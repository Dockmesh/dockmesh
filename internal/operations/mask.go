package operations

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

// Masked replaces every secret in log lines, step details and errors.
const Masked = "********"

// minSecretLen keeps short values (a "1", "on") from masking unrelated
// text; they are not worth protecting by substring replacement anyway.
const minSecretLen = 4

var maskPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// key=value / key: value for names that hold credentials.
	{regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[a-z0-9_.-]*)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&]+)`), "${1}${2}" + Masked},
	{regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|x-api-key|cookie)(\s*[=:]\s*)([^\r\n]+)`), "${1}${2}" + Masked},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} " + Masked},
	// Credentials in URLs: scheme://user:password@host.
	{regexp.MustCompile(`(://[^/\s:@]*:)[^@\s/]+@`), "${1}" + Masked + "@"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), Masked},
}

// MaskText applies the generic credential patterns and replaces each of
// secrets, also in its URL-escaped and base64 forms.
func MaskText(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, Masked)
	}
	for _, p := range maskPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

func (o *Op) mask(s string) string {
	if s == "" {
		return s
	}
	return MaskText(s, o.secrets)
}

// appendSecret adds v and the encodings it is likely to appear in,
// longest first so a value never leaks through a shorter variant
// replacing only part of it.
func appendSecret(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if len(v) < minSecretLen {
		return list
	}
	for _, form := range []string{
		base64.StdEncoding.EncodeToString([]byte(v)),
		base64.URLEncoding.EncodeToString([]byte(v)),
		base64.RawStdEncoding.EncodeToString([]byte(v)),
		base64.RawURLEncoding.EncodeToString([]byte(v)),
		url.QueryEscape(v),
		url.PathEscape(v),
		v,
	} {
		if !contains(list, form) {
			list = append(list, form)
		}
	}
	sortByLenDesc(list)
	return list
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func sortByLenDesc(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && len(list[j]) > len(list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

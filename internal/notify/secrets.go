package notify

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// publicFields lists the config fields of each channel type that are not
// credentials. Every other field, including ones no channel reads, is
// treated as a secret: the API never returns it.
var publicFields = map[string][]string{
	"email":     {"host", "port", "username", "from", "to"},
	"ntfy":      {"priority"},
	"pushover":  {"device", "sound"},
	"pagerduty": {"client", "client_url"},
}

// PublicConfigField reports whether key of a channelType config may be
// shown in API responses.
func PublicConfigField(channelType, key string) bool {
	if key == "language" {
		return true
	}
	for _, f := range publicFields[channelType] {
		if f == key {
			return true
		}
	}
	return false
}

const redactedText = "[redacted]"

// minMaskLen keeps very short secret values from masking unrelated words
// in an error message.
const minMaskLen = 4

// secretValues returns the strings of c's secret fields that could show
// up in an error message. A URL also contributes its path, query and
// password, since receivers echo the request path rather than the URL.
func secretValues(c *Channel) []string {
	var m map[string]any
	if json.Unmarshal(c.Config, &m) != nil {
		return nil
	}
	var out []string
	add := func(s string) {
		if len(s) >= minMaskLen {
			out = append(out, s)
		}
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok || PublicConfigField(c.Type, k) {
			continue
		}
		add(s)
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" {
			continue
		}
		add(u.EscapedPath())
		add(u.Path)
		add(u.RawQuery)
		for _, vals := range u.Query() {
			for _, qv := range vals {
				add(qv)
			}
		}
		if p, ok := u.User.Password(); ok {
			add(p)
		}
	}
	// Longest first, so a URL is masked whole before its path is.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// errorText renders a delivery error for the API, the log and the stored
// last_error. A URL error is reduced to its cause, since its URL can carry
// a webhook token; withBody adds a receiver's error page, which only the
// Test button shows. The channel's secret values are masked either way.
func errorText(err error, withBody bool, secrets []string) string {
	var uerr *url.Error
	var serr *statusError
	var msg string
	switch {
	case errors.As(err, &uerr):
		msg = uerr.Op + ": " + uerr.Err.Error()
	case errors.As(err, &serr):
		msg = serr.Status
		if withBody {
			msg = serr.Error()
		}
	default:
		msg = err.Error()
	}
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, redactedText)
	}
	return msg
}

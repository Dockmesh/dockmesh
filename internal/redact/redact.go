// Package redact keeps stored credentials write-only in API responses.
//
// A response carries every secret field as "" plus "<field>_set": true
// when a value is stored. On update, a secret the client sends empty or
// omits keeps its stored value, so a redacted object can be sent back
// unchanged; any other value replaces it.
package redact

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SetSuffix names the marker field added next to a redacted secret.
const SetSuffix = "_set"

// Map returns a copy of cfg with the secret fields blanked and marked.
func Map(cfg map[string]any, secrets []string) map[string]any {
	if cfg == nil {
		return nil
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, k := range secrets {
		v, ok := out[k]
		if !ok {
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			continue
		}
		out[k] = ""
		out[k+SetSuffix] = true
	}
	return out
}

// ErrReenter is returned by Keep when a stored secret would be reused
// against a different endpoint or identity.
var ErrReenter = errors.New("re-enter the credentials when changing the server, URL or user")

// Bound lists the fields that say where and as whom a secret is used. A
// stored secret is only kept while none of them changes, so editing them
// can't send the stored secret somewhere else.
var Bound = []string{"host", "port", "url", "endpoint", "username", "access_key", "share", "from"}

// ErrFieldName is returned for a config field name that encoding/json
// could match to a different struct field than the one it spells.
var ErrFieldName = errors.New("config field name is ambiguous")

// CheckKeys refuses field names a struct decoder would fold onto another
// field: non-ASCII names (json folds "ſ" to "s" and the Kelvin sign to
// "k"), names that differ only in case from another name in cfg, and
// case variants of known or Bound names. Otherwise a second "Host" or
// "hoſt" could redirect a config that compares equal on "host".
func CheckKeys(cfg map[string]any, known []string) error {
	canon := canonicalNames(known)
	seen := make(map[string]bool, len(cfg))
	for k := range cfg {
		low := strings.ToLower(k)
		if ambiguous(k, canon) || seen[low] {
			return fmt.Errorf("%w: %q", ErrFieldName, k)
		}
		seen[low] = true
	}
	return nil
}

func canonicalNames(known []string) map[string]string {
	canon := make(map[string]string, len(known)+len(Bound))
	for _, list := range [][]string{Bound, known} {
		for _, k := range list {
			canon[strings.ToLower(k)] = k
		}
	}
	return canon
}

func ambiguous(k string, canon map[string]string) bool {
	for i := 0; i < len(k); i++ {
		if k[i] >= utf8.RuneSelf {
			return true
		}
	}
	c, ok := canon[strings.ToLower(k)]
	return ok && c != k
}

// Keep returns next with empty (missing, null or "") secret fields taken
// from prev and the "<field>_set" markers removed; a secret sent with
// "<field>_set": false is cleared instead. It fails with ErrFieldName for
// field names CheckKeys refuses and with ErrReenter when a secret would
// be kept while a Bound field differs from prev; a Bound field that is a
// secret itself counts too, so a new URL can't take a kept token along.
func Keep(next, prev map[string]any, secrets []string) (map[string]any, error) {
	if err := CheckKeys(next, secrets); err != nil {
		return nil, err
	}
	canon := canonicalNames(secrets)
	out := make(map[string]any, len(next))
	for k, v := range next {
		out[k] = v
	}
	kept := false
	for _, k := range secrets {
		wipe := out[k+SetSuffix] == false
		delete(out, k+SetSuffix)
		if v := out[k]; v != nil && v != "" {
			continue
		}
		if wipe {
			delete(out, k)
			continue
		}
		// A legacy stored name the decoder would fold elsewhere isn't
		// carried over.
		if ambiguous(k, canon) {
			continue
		}
		if pv, ok := prev[k]; ok {
			out[k] = pv
			if s, _ := pv.(string); s != "" {
				kept = true
			}
		}
	}
	if kept {
		for _, k := range Bound {
			if fmt.Sprint(out[k]) != fmt.Sprint(prev[k]) {
				return nil, ErrReenter
			}
		}
	}
	return out, nil
}

// ToMap decodes a JSON-shaped value (a decoded struct field typed any, or
// raw JSON) into a map. Anything that isn't a JSON object yields nil.
func ToMap(v any) map[string]any {
	var raw []byte
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return t
	case json.RawMessage:
		raw = t
	case []byte:
		raw = t
	case string:
		raw = []byte(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = b
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

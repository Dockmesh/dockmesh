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
)

// SetSuffix names the marker field added next to a redacted secret.
const SetSuffix = "_set"

// Map returns a copy of cfg with the secret fields blanked and marked.
func Map(cfg map[string]any, secrets []string) map[string]any {
	if cfg == nil {
		return nil
	}
	out := make(map[string]any, len(cfg)+len(secrets))
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

// Keep returns next with empty or missing secret fields taken from prev
// and the "<field>_set" markers removed. It fails with ErrReenter when a
// secret would be kept while a Bound field differs from prev.
func Keep(next, prev map[string]any, secrets []string) (map[string]any, error) {
	out := make(map[string]any, len(next))
	for k, v := range next {
		out[k] = v
	}
	isSecret := make(map[string]bool, len(secrets))
	kept := false
	for _, k := range secrets {
		isSecret[k] = true
		delete(out, k+SetSuffix)
		if s, _ := out[k].(string); s != "" {
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
			if !isSecret[k] && fmt.Sprint(out[k]) != fmt.Sprint(prev[k]) {
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

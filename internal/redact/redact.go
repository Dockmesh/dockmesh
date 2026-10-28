// Package redact keeps stored credentials write-only in API responses.
//
// A response carries every secret field as "" plus "<field>_set": true
// when a value is stored. On update, a secret the client sends empty or
// omits keeps its stored value, so a redacted object can be sent back
// unchanged; any other value replaces it.
package redact

import "encoding/json"

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

// Keep returns next with empty or missing secret fields taken from prev
// and the "<field>_set" markers removed.
func Keep(next, prev map[string]any, secrets []string) map[string]any {
	out := make(map[string]any, len(next))
	for k, v := range next {
		out[k] = v
	}
	for _, k := range secrets {
		delete(out, k+SetSuffix)
		if s, _ := out[k].(string); s != "" {
			continue
		}
		if pv, ok := prev[k]; ok {
			out[k] = pv
		}
	}
	return out
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

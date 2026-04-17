// Package openapi serves and verifies the hand-maintained OpenAPI 3.1
// spec at docs/openapi.yaml.
//
// The spec is embedded into the binary at build time — no disk lookup
// in production. The server refuses to boot if the embedded file fails
// to parse, so a spec syntax error is a loud failure, not a silent one.
//
// P.11.10. See CLAUDE.md "OpenAPI Contract" for the maintenance rule.
package openapi

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// rawYAML is the literal bytes of docs/openapi.yaml shipped in every
// Dockmesh binary. Embedded at build time via //go:embed below.
//
//go:embed openapi.yaml
var rawYAML []byte

// Spec is a minimal view of the bits we read programmatically. We keep
// it small so a schema addition in the YAML doesn't force a Go struct
// change — anything not in Spec passes through as a raw yaml.Node and
// is re-emitted byte-for-byte by Bytes() / JSON().
type Spec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title   string `yaml:"title"`
		Version string `yaml:"version"`
	} `yaml:"info"`
	// Paths is a map of path → (method → op). Kept as yaml.Node so we
	// don't have to model every OpenAPI field — the drift test only
	// needs the top-level keys (path + method).
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// Load parses the embedded YAML once at startup. Callers hold the
// result for the lifetime of the process.
func Load() (*Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(rawYAML, &s); err != nil {
		return nil, fmt.Errorf("parse openapi.yaml: %w", err)
	}
	if s.OpenAPI == "" {
		return nil, fmt.Errorf("openapi.yaml missing `openapi` version field")
	}
	return &s, nil
}

// YAMLBytes returns the raw YAML exactly as shipped. Used by the
// /openapi.yaml serving endpoint.
func YAMLBytes() []byte {
	// Return a copy so callers can't mutate the embedded slice.
	out := make([]byte, len(rawYAML))
	copy(out, rawYAML)
	return out
}

// JSONBytes converts the embedded YAML to pretty-printed JSON. The
// YAML → JSON hop loses YAML-specific features (anchors, tags) but
// OpenAPI never uses those, so the conversion is lossless for our
// purposes. Cached on first call.
var cachedJSON []byte

func JSONBytes() ([]byte, error) {
	if cachedJSON != nil {
		return cachedJSON, nil
	}
	var generic any
	if err := yaml.Unmarshal(rawYAML, &generic); err != nil {
		return nil, fmt.Errorf("parse for json: %w", err)
	}
	// yaml.Unmarshal into any gives map[any]any; JSON encoder rejects
	// non-string map keys so we convert.
	normalised := normaliseKeys(generic)
	b, err := json.MarshalIndent(normalised, "", "  ")
	if err != nil {
		return nil, err
	}
	cachedJSON = b
	return b, nil
}

// Operations returns the set of (method, path) pairs declared in the
// spec. Methods are upper-cased so they compare directly against chi's
// route inventory. Used by TestOpenAPIDriftAgainstRoutes.
type Operation struct {
	Method string // "GET"
	Path   string // "/containers/{id}"
}

func (s *Spec) Operations() []Operation {
	out := []Operation{}
	for path, methods := range s.Paths {
		for m := range methods {
			out = append(out, Operation{Method: upperASCII(m), Path: path})
		}
	}
	return out
}

// normaliseKeys walks a YAML-parsed tree (map[any]any) and replaces
// non-string keys with their fmt.Sprintf'd form so encoding/json is
// happy. OpenAPI docs never actually have non-string keys, but
// yaml.v3's generic Unmarshal loses the type information.
func normaliseKeys(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			m[fmt.Sprintf("%v", k)] = normaliseKeys(vv)
		}
		return m
	case map[string]any:
		for k, vv := range x {
			x[k] = normaliseKeys(vv)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normaliseKeys(x[i])
		}
		return x
	default:
		return v
	}
}

// upperASCII is a tiny helper so we don't pull in unicode just for
// four characters ("get" → "GET").
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

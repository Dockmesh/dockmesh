// Package templates implements the stack-template feature (P.11.12):
// compose.yaml snippets with `{{param}}` placeholders that the server
// substitutes at deploy time.
//
// The template syntax is intentionally minimal — we do NOT use Go's
// text/template because it's Turing-complete and operators would end
// up executing arbitrary code via user-contributed templates. Our
// parser only does literal-text / placeholder substitution; validation
// happens in the Param schema, not the template.
//
// Syntax:
//
//	{{name}}
//	{{name|default:foo bar}}
//	{{name|secret}}                           — auto-generate if not provided
//	{{name|enum:a,b,c}}
//	{{name|pattern:^[a-z]+$}}
//
// Multiple flags are pipe-separated; order doesn't matter.
package templates

import (
	"fmt"
	"regexp"
	"strings"
)

// ParamDef describes one template parameter, stored as JSON alongside
// the template body.
type ParamDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`         // "string" (default) | "number" | "bool" | "secret"
	Default     string   `json:"default,omitempty"`
	Secret      bool     `json:"secret,omitempty"`       // generate a random value if not supplied
	Enum        []string `json:"enum,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`      // regex
	Required    bool     `json:"required,omitempty"`
}

// Parse walks a template body and returns the parameter definitions it
// references, merged with the explicit list in `declared`. Explicit
// defs override inline flags on the same name — the declared list is
// the source of truth for descriptions, types, and ordering.
func Parse(body string, declared []ParamDef) ([]ParamDef, error) {
	inline := extractInline(body)
	byName := make(map[string]*ParamDef, len(declared))
	for i := range declared {
		d := declared[i]
		byName[d.Name] = &d
	}
	var out []ParamDef
	seen := make(map[string]struct{}, len(inline))
	// Keep order: declared first, then inline-discovered.
	for _, d := range declared {
		out = append(out, d)
		seen[d.Name] = struct{}{}
	}
	for _, p := range inline {
		if _, ok := seen[p.Name]; ok {
			continue
		}
		out = append(out, p)
		seen[p.Name] = struct{}{}
	}
	// Validate regex patterns once — better to fail at template save
	// than at deploy.
	for _, p := range out {
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return nil, fmt.Errorf("parameter %q: invalid pattern: %w", p.Name, err)
			}
		}
	}
	return out, nil
}

// placeholder finds `{{ ... }}` non-greedily. We match any content
// up to the next `}}`, which allows regex patterns that contain
// literal braces like `{2,5}` inside `pattern:` flags.
var placeholderRE = regexp.MustCompile(`\{\{\s*(.+?)\s*\}\}`)

func extractInline(body string) []ParamDef {
	matches := placeholderRE.FindAllStringSubmatch(body, -1)
	byName := map[string]ParamDef{}
	var order []string
	for _, m := range matches {
		inner := strings.TrimSpace(m[1])
		def := parseInline(inner)
		if def.Name == "" {
			continue
		}
		if _, ok := byName[def.Name]; !ok {
			order = append(order, def.Name)
		}
		// Merge — later occurrences may add flags the first didn't have.
		existing := byName[def.Name]
		if def.Default != "" {
			existing.Default = def.Default
		}
		if def.Secret {
			existing.Secret = true
		}
		if def.Pattern != "" {
			existing.Pattern = def.Pattern
		}
		if len(def.Enum) > 0 {
			existing.Enum = def.Enum
		}
		existing.Name = def.Name
		byName[def.Name] = existing
	}
	out := make([]ParamDef, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

func parseInline(s string) ParamDef {
	parts := strings.Split(s, "|")
	def := ParamDef{Name: strings.TrimSpace(parts[0])}
	if def.Name == "" {
		return def
	}
	for _, flag := range parts[1:] {
		flag = strings.TrimSpace(flag)
		switch {
		case strings.HasPrefix(flag, "default:"):
			def.Default = strings.TrimPrefix(flag, "default:")
		case flag == "secret":
			def.Secret = true
			def.Type = "secret"
		case strings.HasPrefix(flag, "pattern:"):
			def.Pattern = strings.TrimPrefix(flag, "pattern:")
		case strings.HasPrefix(flag, "enum:"):
			raw := strings.TrimPrefix(flag, "enum:")
			for _, v := range strings.Split(raw, ",") {
				v = strings.TrimSpace(v)
				if v != "" {
					def.Enum = append(def.Enum, v)
				}
			}
		case flag == "required":
			def.Required = true
		}
	}
	return def
}

// Render substitutes every `{{name[|...]}}` in the body with the
// matching value from `values`, applying defaults and validating
// against pattern / enum. Missing required values produce an error.
func Render(body string, params []ParamDef, values map[string]string) (string, error) {
	byName := make(map[string]ParamDef, len(params))
	for _, p := range params {
		byName[p.Name] = p
	}
	var rerr error
	out := placeholderRE.ReplaceAllStringFunc(body, func(raw string) string {
		m := placeholderRE.FindStringSubmatch(raw)
		if m == nil {
			return raw
		}
		def := parseInline(strings.TrimSpace(m[1]))
		if p, ok := byName[def.Name]; ok {
			def = mergeDefs(p, def)
		}
		val, ok := values[def.Name]
		if !ok || val == "" {
			val = def.Default
		}
		if val == "" {
			if def.Secret {
				// Generators defer to the caller so the secrets
				// service is available at deploy time — we flag
				// missing secrets here and let the deploy handler
				// fill them in before calling Render again.
				if rerr == nil {
					rerr = fmt.Errorf("secret parameter %q not provided", def.Name)
				}
				return raw
			}
			if def.Required {
				if rerr == nil {
					rerr = fmt.Errorf("required parameter %q not provided", def.Name)
				}
				return raw
			}
		}
		if err := validate(def, val); err != nil {
			if rerr == nil {
				rerr = err
			}
			return raw
		}
		return val
	})
	if rerr != nil {
		return "", rerr
	}
	return out, nil
}

// mergeDefs overlays inline flags onto an explicit declaration —
// explicit wins on name / description / type / required; inline may
// add flags not declared explicitly.
func mergeDefs(explicit, inline ParamDef) ParamDef {
	if explicit.Default == "" {
		explicit.Default = inline.Default
	}
	if !explicit.Secret {
		explicit.Secret = inline.Secret
	}
	if explicit.Pattern == "" {
		explicit.Pattern = inline.Pattern
	}
	if len(explicit.Enum) == 0 {
		explicit.Enum = inline.Enum
	}
	return explicit
}

func validate(def ParamDef, val string) error {
	if val == "" {
		return nil
	}
	if len(def.Enum) > 0 {
		for _, v := range def.Enum {
			if v == val {
				return nil
			}
		}
		return fmt.Errorf("parameter %q: %q not in enum [%s]", def.Name, val, strings.Join(def.Enum, ","))
	}
	if def.Pattern != "" {
		re, err := regexp.Compile(def.Pattern)
		if err != nil {
			return fmt.Errorf("parameter %q: bad pattern: %w", def.Name, err)
		}
		if !re.MatchString(val) {
			return fmt.Errorf("parameter %q: value %q does not match /%s/", def.Name, val, def.Pattern)
		}
	}
	return nil
}

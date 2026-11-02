package compose

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/template"
	"gopkg.in/yaml.v3"
)

// ImagesOf lists the service images of a compose file, with ${VAR} taken
// from envContent only. It reads nothing but the given text, so it is safe
// for compose that is not going to run on this machine.
func ImagesOf(composeYAML, envContent string) []string {
	var doc struct {
		Services map[string]struct {
			Image any `yaml:"image"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil {
		return nil
	}
	env := map[string]string{}
	for _, kv := range parseEnvContent(envContent) {
		k, v, _ := strings.Cut(kv, "=")
		env[strings.TrimSpace(k)] = v
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	var out []string
	for _, svc := range doc.Services {
		img, ok := svc.Image.(string)
		if !ok || img == "" {
			continue
		}
		if s, err := template.Substitute(img, lookup); err == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

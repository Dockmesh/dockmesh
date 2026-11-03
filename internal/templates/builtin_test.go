package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderBuiltin renders a built-in template the way Materialize does,
// with generated values for every secret parameter.
func renderBuiltin(t *testing.T, file string, values map[string]string) string {
	t.Helper()
	data, err := builtinFS.ReadFile("builtin/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var rec builtinRecord
	if err := yaml.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	params, err := Parse(rec.Compose+"\n"+rec.Env, rec.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	resolved := map[string]string{}
	for k, v := range values {
		resolved[k] = v
	}
	for _, p := range params {
		if _, ok := resolved[p.Name]; !ok && p.Secret {
			gen, err := genSecret(32)
			if err != nil {
				t.Fatal(err)
			}
			resolved[p.Name] = gen
		}
	}
	out, err := Render(rec.Compose, params, resolved)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type renderedCompose struct {
	Services map[string]struct {
		Command     []string          `yaml:"command"`
		Environment map[string]string `yaml:"environment"`
		Ports       []string          `yaml:"ports"`
	} `yaml:"services"`
}

func TestRedisTemplateDefaults(t *testing.T) {
	tests := []struct {
		name     string
		values   map[string]string
		wantPort string
	}{
		{"defaults", nil, "127.0.0.1:6379:6379"},
		{"all interfaces", map[string]string{"bind_address": "0.0.0.0", "port": "6380"}, "0.0.0.0:6380:6379"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c renderedCompose
			if err := yaml.Unmarshal([]byte(renderBuiltin(t, "redis.yaml", tt.values)), &c); err != nil {
				t.Fatal(err)
			}
			svc := c.Services["redis"]
			if len(svc.Ports) != 1 || svc.Ports[0] != tt.wantPort {
				t.Errorf("ports %v, want %s", svc.Ports, tt.wantPort)
			}
			cmd := strings.Join(svc.Command, " ")
			pw := svc.Environment["REDISCLI_AUTH"]
			if pw == "" || !strings.Contains(cmd, "--requirepass "+pw) {
				t.Errorf("command %q does not require the generated password", cmd)
			}
		})
	}
}

// A password is one argument of redis-server; anything that could split
// into more arguments is refused.
func TestRedisTemplateRejectsArgumentPasswords(t *testing.T) {
	data, err := builtinFS.ReadFile("builtin/redis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var rec builtinRecord
	if err := yaml.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	params, err := Parse(rec.Compose, rec.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"secret --protected-mode no", `abc", "--port", "1`, "short"} {
		if _, err := Render(rec.Compose, params, map[string]string{"password": pw}); err == nil {
			t.Errorf("password %q accepted", pw)
		}
	}
}

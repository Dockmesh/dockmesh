package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadBuiltin(t *testing.T, file string) (builtinRecord, []ParamDef) {
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
	return rec, params
}

// renderBuiltin renders a built-in template's compose and env the way
// Materialize does, with generated values for every secret parameter.
func renderBuiltin(t *testing.T, file string, values map[string]string) (compose, env string) {
	t.Helper()
	rec, params := loadBuiltin(t, file)
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
	compose, err := Render(rec.Compose, params, resolved)
	if err != nil {
		t.Fatal(err)
	}
	env, err = Render(rec.Env, params, resolved)
	if err != nil {
		t.Fatal(err)
	}
	return compose, env
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
			compose, env := renderBuiltin(t, "redis.yaml", tt.values)
			var c renderedCompose
			if err := yaml.Unmarshal([]byte(compose), &c); err != nil {
				t.Fatal(err)
			}
			svc := c.Services["redis"]
			if len(svc.Ports) != 1 || svc.Ports[0] != tt.wantPort {
				t.Errorf("ports %v, want %s", svc.Ports, tt.wantPort)
			}
			pw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(env), "REDIS_PASSWORD="))
			if len(pw) < 12 {
				t.Fatalf("no generated password in .env: %q", env)
			}
			// Inspect shows the command to Containers: View; the password
			// belongs in .env and the environment only.
			if strings.Contains(compose, pw) {
				t.Error("password rendered into compose.yaml")
			}
			cmd := strings.Join(svc.Command, " ")
			if !strings.Contains(cmd, "requirepass") || !strings.Contains(cmd, "redis-server -") {
				t.Errorf("command %q does not pass requirepass on stdin", cmd)
			}
			if svc.Environment["REDISCLI_AUTH"] != "${REDIS_PASSWORD}" {
				t.Errorf("REDISCLI_AUTH = %q", svc.Environment["REDISCLI_AUTH"])
			}
		})
	}
}

// The password goes into .env and a redis config line; anything that
// could add another line or setting is refused, without echoing it.
func TestRedisTemplateRejectsArgumentPasswords(t *testing.T) {
	rec, params := loadBuiltin(t, "redis.yaml")
	for _, pw := range []string{"secret --protected-mode no", "abc\nrequirepass x", "short"} {
		_, err := Render(rec.Env, params, map[string]string{"password": pw})
		if err == nil {
			t.Errorf("password %q accepted", pw)
		} else if strings.Contains(err.Error(), pw) {
			t.Errorf("error echoes the secret: %v", err)
		}
	}
}

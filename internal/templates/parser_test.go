package templates

import (
	"strings"
	"testing"
)

func TestParseInline(t *testing.T) {
	body := `POSTGRES_USER={{db_user|default:postgres}}
POSTGRES_PASSWORD={{db_password|secret}}
POSTGRES_PORT={{port|default:5432|pattern:^[0-9]{2,5}$}}
`
	params, err := Parse(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 3 {
		t.Fatalf("want 3 params, got %d: %+v", len(params), params)
	}
	byName := map[string]ParamDef{}
	for _, p := range params {
		byName[p.Name] = p
	}
	if byName["db_user"].Default != "postgres" {
		t.Errorf("db_user default = %q", byName["db_user"].Default)
	}
	if !byName["db_password"].Secret {
		t.Errorf("db_password should be secret")
	}
	if byName["port"].Pattern != `^[0-9]{2,5}$` {
		t.Errorf("port pattern = %q", byName["port"].Pattern)
	}
}

func TestRender_HappyPath(t *testing.T) {
	body := `user: {{u}}
password: {{p|secret}}`
	params := []ParamDef{
		{Name: "u", Default: "alice"},
		{Name: "p", Secret: true},
	}
	out, err := Render(body, params, map[string]string{"p": "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user: alice") {
		t.Errorf("missing default: %q", out)
	}
	if !strings.Contains(out, "password: hunter2") {
		t.Errorf("missing pwd: %q", out)
	}
}

func TestRender_MissingSecretFails(t *testing.T) {
	body := `token: {{t|secret}}`
	_, err := Render(body, []ParamDef{{Name: "t", Secret: true}}, nil)
	if err == nil {
		t.Fatalf("want error for missing secret")
	}
}

func TestRender_EnumRejectsInvalidValue(t *testing.T) {
	body := `env: {{env|enum:dev,prod}}`
	_, err := Render(body, nil, map[string]string{"env": "staging"})
	if err == nil {
		t.Fatalf("want enum error")
	}
}

func TestRender_PatternRejectsInvalid(t *testing.T) {
	body := `port: {{port|pattern:^[0-9]+$}}`
	_, err := Render(body, nil, map[string]string{"port": "abc"})
	if err == nil {
		t.Fatalf("want pattern error")
	}
}

package compose

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateEnvironmentName(t *testing.T) {
	good := []string{"", "prod", "staging", "dev-1", "dev_1", "pre-prod"}
	for _, g := range good {
		if err := ValidateEnvironmentName(g); err != nil {
			t.Errorf("ValidateEnvironmentName(%q) rejected: %v", g, err)
		}
	}
	bad := []string{"Prod", "has space", "dot.in.name", "-leading", "_leading", "has/slash"}
	for _, b := range bad {
		if err := ValidateEnvironmentName(b); err == nil {
			t.Errorf("ValidateEnvironmentName(%q) accepted", b)
		}
	}
}

func TestDiscoverEnvironments(t *testing.T) {
	dir := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("compose.yaml", "services:\n  web:\n    image: nginx:1.25\n")
	must("compose.prod.yaml", "services:\n  web:\n    image: nginx:1.26\n")
	must("compose.staging.yaml", "services:\n  web:\n    image: nginx:1.25-alpine\n")
	must("compose.backup.yml.disabled", "ignore me")
	must("README.md", "ignore me")

	got, err := DiscoverEnvironments(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prod", "staging"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestMergeEnvironment_BaseOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx:1.25\n"), 0o644)

	_, merged, err := MergeEnvironment(context.Background(), dir, "app", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "nginx:1.25") {
		t.Fatalf("merged missing base image: %s", merged)
	}
}

func TestMergeEnvironment_WithOverride(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("services:\n  web:\n    image: nginx:1.25\n    environment:\n      - TIER=base\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "compose.prod.yaml"),
		[]byte("services:\n  web:\n    image: nginx:1.26\n    environment:\n      - TIER=prod\n"), 0o644)

	_, merged, err := MergeEnvironment(context.Background(), dir, "app", "", "prod", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "nginx:1.26") {
		t.Fatalf("override didn't win: %s", merged)
	}
	// compose-go normalises env-list into a map, so the merged yaml
	// renders `TIER: prod` rather than `- TIER=prod`. Either form is
	// fine; we just need the value to come from the override.
	if !strings.Contains(merged, "TIER: prod") && !strings.Contains(merged, "TIER=prod") {
		t.Fatalf("override env didn't win: %s", merged)
	}
}

func TestMergeEnvironment_MissingOverride(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx:1.25\n"), 0o644)

	_, _, err := MergeEnvironment(context.Background(), dir, "app", "", "prod", false)
	if err == nil {
		t.Fatal("want error for missing override")
	}
	if !strings.Contains(err.Error(), "environment override not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeEnvironment_BadName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx:1.25\n"), 0o644)

	_, _, err := MergeEnvironment(context.Background(), dir, "app", "", "../../etc/passwd", false)
	if err == nil {
		t.Fatal("want error for bad name")
	}
}

const pathsBase = `services:
  web:
    image: nginx
    volumes:
      - ./html:/html
      - ../shared:/shared
      - /srv/abs:/abs
      - .:/app
    build:
      context: ./src
    configs: [cfg]
    secrets: [key]
configs:
  cfg:
    file: ./cfg.txt
secrets:
  key:
    file: ../keys/key.pem
`

const pathsOverride = "services:\n  web:\n    image: nginx:1.26\n    volumes:\n      - ./prod-data:/data\n"

func TestMergeEnvironmentPaths(t *testing.T) {
	stackDir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"compose.yaml": pathsBase, "compose.prod.yaml": pathsOverride} {
		if err := os.WriteFile(filepath.Join(stackDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parent := filepath.Dir(stackDir)
	merge := map[string]func(keepRelative bool) (string, error){
		"files": func(keepRelative bool) (string, error) {
			_, out, err := MergeEnvironment(context.Background(), stackDir, "app", "", "prod", keepRelative)
			return out, err
		},
		"content": func(keepRelative bool) (string, error) {
			return MergeEnvironmentContent(context.Background(), stackDir, "app", "", "prod", []byte(pathsBase), []byte(pathsOverride), keepRelative)
		},
	}
	tests := []struct {
		name         string
		keepRelative bool
		want         map[string]string
	}{
		{"relative paths stay relative", true, map[string]string{
			"/html": "./html", "/shared": "../shared", "/abs": "/srv/abs", "/app": ".", "/data": "./prod-data",
			"context": "./src", "config": "./cfg.txt", "secret": "../keys/key.pem",
		}},
		{"resolved against the stack directory", false, map[string]string{
			"/html": filepath.Join(stackDir, "html"), "/shared": filepath.Join(parent, "shared"), "/abs": "/srv/abs",
			"/app": stackDir, "/data": filepath.Join(stackDir, "prod-data"), "context": filepath.Join(stackDir, "src"),
			"config": filepath.Join(stackDir, "cfg.txt"), "secret": filepath.Join(parent, "keys", "key.pem"),
		}},
	}
	for _, tt := range tests {
		for via, fn := range merge {
			t.Run(tt.name+"/"+via, func(t *testing.T) {
				out, err := fn(tt.keepRelative)
				if err != nil {
					t.Fatal(err)
				}
				if got := mergedPaths(t, out); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("paths = %v\nwant    %v\nyaml:\n%s", got, tt.want, out)
				}
			})
		}
	}
}

// mergedPaths reads the bind sources by target and the other file paths
// of service web back from a merged compose file.
func mergedPaths(t *testing.T, merged string) map[string]string {
	t.Helper()
	var doc struct {
		Services map[string]struct {
			Volumes []struct{ Source, Target string }
			Build   struct{ Context string }
		}
		Configs map[string]struct{ File string }
		Secrets map[string]struct{ File string }
	}
	if err := yaml.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	web := doc.Services["web"]
	for _, v := range web.Volumes {
		out[v.Target] = v.Source
	}
	out["context"] = web.Build.Context
	out["config"] = doc.Configs["cfg"].File
	out["secret"] = doc.Secrets["key"].File
	return out
}

func TestMergeEnvironmentKeepsEnvFileRelative(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"compose.yaml":      "services:\n  web:\n    image: nginx\n    env_file: [web.env]\n",
		"compose.prod.yaml": "services:\n  web:\n    image: nginx:1.26\n",
		"web.env":           "A=1\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, out, err := MergeEnvironment(context.Background(), dir, "app", "", "prod", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path: web.env") || strings.Contains(out, dir) {
		t.Fatalf("env_file not kept relative:\n%s", out)
	}
}

package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	_, merged, err := MergeEnvironment(context.Background(), dir, "app", "", "")
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

	_, merged, err := MergeEnvironment(context.Background(), dir, "app", "", "prod")
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

	_, _, err := MergeEnvironment(context.Background(), dir, "app", "", "prod")
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

	_, _, err := MergeEnvironment(context.Background(), dir, "app", "", "../../etc/passwd")
	if err == nil {
		t.Fatal("want error for bad name")
	}
}

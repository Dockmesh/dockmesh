package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func TestSyncAndDeleteStackContainment(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../outside", "..", "a/../../outside", "/etc", "Web", ""} {
		if err := syncStack(base, agents.StackSyncReq{Name: name, Compose: "services: {}"}); err == nil {
			t.Errorf("syncStack accepted %q", name)
		}
		if err := deleteStack(base, name); err == nil {
			t.Errorf("deleteStack accepted %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("file outside the stacks dir was touched: %v", err)
	}

	req := agents.StackSyncReq{Name: "web", Compose: "services: {}", Env: "A=1", Meta: "{}"}
	if err := syncStack(base, req); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"compose.yaml", ".env", ".dockmesh.meta.json"} {
		if _, err := os.Stat(filepath.Join(base, "stacks", "web", f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	req.Env = ""
	if err := syncStack(base, req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "stacks", "web", ".env")); !os.IsNotExist(err) {
		t.Errorf("stale .env kept: %v", err)
	}
	if err := deleteStack(base, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "stacks", "web")); !os.IsNotExist(err) {
		t.Errorf("stack dir not removed: %v", err)
	}
	if err := deleteStack(base, "never-existed"); err != nil {
		t.Errorf("deleting a missing stack: %v", err)
	}
}

func TestValidateVolumeName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"data", true},
		{"web_db-data.1", true},
		{"a", false},
		{"/etc", false},
		{"/", false},
		{"../x", false},
		{"vol:/host", false},
		{"", false},
	}
	for _, c := range cases {
		if err := validateVolumeName(c.name); (err == nil) != c.ok {
			t.Errorf("validateVolumeName(%q) err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

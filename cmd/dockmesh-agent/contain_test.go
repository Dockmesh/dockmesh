package main

import (
	"context"
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
		if err := deleteStack(context.Background(), nil, base, name); err == nil {
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
	for _, f := range []string{"compose.yaml", ".dockmesh.meta.json"} {
		if _, err := os.Stat(filepath.Join(base, "stacks", "web", f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "stacks", "web", ".env")); !os.IsNotExist(err) {
		t.Errorf(".env mirrored: %v", err)
	}
	if err := deleteStack(context.Background(), nil, base, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "stacks", "web")); !os.IsNotExist(err) {
		t.Errorf("stack dir not removed: %v", err)
	}
	if err := deleteStack(context.Background(), nil, base, "never-existed"); err != nil {
		t.Errorf("deleting a missing stack: %v", err)
	}
}

func TestSyncKeepsOldEnvCopy(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "stacks", "web")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if !hasMirroredEnv(base) {
		t.Fatal("old .env copy not detected")
	}
	for _, env := range []string{"", "A=2\n"} {
		if err := syncStack(base, agents.StackSyncReq{Name: "web", Compose: "services: {}", Env: env}); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(filepath.Join(dir, ".env")); err != nil || string(b) != "A=1\n" {
			t.Fatalf("sync touched the old .env copy: %q, %v", b, err)
		}
	}
}

func TestRemoveMirroredEnv(t *testing.T) {
	tests := []struct {
		name  string
		binds func(t *testing.T, dir string) []string
		keep  bool
	}{
		{"no binds", func(*testing.T, string) []string { return nil }, false},
		{"other bind", func(t *testing.T, dir string) []string { return []string{filepath.Join(dir, "data"), "/srv/media"} }, false},
		{"env file bind", func(t *testing.T, dir string) []string { return []string{filepath.Join(dir, ".env")} }, true},
		{"stack dir bind", func(t *testing.T, dir string) []string { return []string{dir + "/"} }, true},
		{"sibling with same prefix", func(t *testing.T, dir string) []string { return []string{filepath.Join(dir, ".env.d")} }, false},
		{"host root", func(*testing.T, string) []string { return []string{"/"} }, true},
		{"stack dir through a symlink", func(t *testing.T, dir string) []string {
			link := filepath.Join(filepath.Dir(dir), "alias")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return []string{link}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "stacks", "web")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			env := filepath.Join(dir, ".env")
			if err := os.WriteFile(env, []byte("A=1\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := removeMirroredEnv(base, "web", tt.binds(t, dir)); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(env)
			if kept := err == nil; kept != tt.keep {
				t.Fatalf("kept = %v, want %v (%v)", kept, tt.keep, err)
			}
			if !tt.keep && hasMirroredEnv(base) {
				t.Error("copy still reported after removal")
			}
		})
	}
	if err := removeMirroredEnv(t.TempDir(), "web", nil); err != nil {
		t.Errorf("stack without a copy: %v", err)
	}
	if err := removeMirroredEnv(t.TempDir(), "../x", nil); err == nil {
		t.Error("invalid name accepted")
	}
}

func TestHasMirroredEnvIgnoresDirectories(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "stacks", "web", ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasMirroredEnv(base) {
		t.Error("a .env directory created by Docker counted as a copy")
	}
}

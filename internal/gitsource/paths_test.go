package gitsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateInputPathInRepo(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"", true},
		{".", true},
		{"deploy/prod", true},
		{"deploy/prod/compose.yaml", true},
		{"..", false},
		{"../../data", false},
		{"deploy/../../x", false},
		{"/etc", false},
	}
	for _, tt := range tests {
		err := validateInput(Input{RepoURL: "https://example.com/r.git", PathInRepo: tt.path})
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.path, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrPathOutsideRepo) {
			t.Errorf("%q: err = %v, want ErrPathOutsideRepo", tt.path, err)
		}
	}
}

// repo builds a clone dir with a compose file, an in-repo symlink and links
// that point at a "server" directory next to it.
func repo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	clone := filepath.Join(base, "clone")
	server := filepath.Join(base, "server")
	for _, d := range []string{filepath.Join(clone, "app", "conf"), server} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(clone, "app", "compose.yaml"):  "services: {}",
		filepath.Join(clone, "app", "conf", "a.yml"): "a",
		filepath.Join(server, "secrets.age-key"):     "KEY",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		filepath.Join(clone, "app", "conf", "b.yml"): "a.yml",
		filepath.Join(clone, "app", "key"):           filepath.Join(server, "secrets.age-key"),
		filepath.Join(clone, "data"):                 server,
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks not available: %v", err)
		}
	}
	return clone
}

func TestInClone(t *testing.T) {
	clone := repo(t)
	tests := []struct {
		p  string
		ok bool
	}{
		{"app", true},
		{"app/conf/b.yml", true},
		{filepath.Join(clone, "app", "compose.yaml"), true},
		{"app/key", false},
		{"data", false},
		{"data/secrets.age-key", false},
		{"../server", false},
	}
	for _, tt := range tests {
		_, err := inClone(clone, tt.p)
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.p, err, tt.ok)
		}
	}
}

func TestCopyTreeSiblingsSkipsLinksOutOfRepo(t *testing.T) {
	clone := repo(t)
	dst := t.TempDir()
	if err := copyTreeSiblings(clone, filepath.Join(clone, "app"), dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "conf", "b.yml")); err != nil || string(b) != "a" {
		t.Errorf("in-repo symlink not copied as its target: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "key")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink to a server file was copied: %v", err)
	}
}

// The clone cache sits under the data dir, which defaults to a relative
// path (./data); symlink resolution must not make every path look foreign.
func TestInCloneWithRelativeCloneDir(t *testing.T) {
	clone := repo(t)
	t.Chdir(filepath.Dir(clone))
	rel := filepath.Base(clone)
	src, err := inClone(rel, "app")
	if err != nil {
		t.Fatalf("relative clone dir: %v", err)
	}
	if _, err := inClone(rel, filepath.Join(src, "compose.yaml")); err != nil {
		t.Errorf("absolute path from an earlier inClone: %v", err)
	}
	if _, err := inClone(rel, "app/key"); !errors.Is(err, ErrPathOutsideRepo) {
		t.Errorf("escape through a relative clone dir: %v", err)
	}
}

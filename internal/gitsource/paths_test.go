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

func openRepo(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestReadCompose(t *testing.T) {
	clone := repo(t)
	server := filepath.Join(filepath.Dir(clone), "server")
	if err := os.MkdirAll(filepath.Join(clone, "evil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(server, "secrets.age-key"), filepath.Join(clone, "evil", "compose.yaml")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	r := openRepo(t, clone)
	got, err := readCompose(r, "app")
	if err != nil || got.compose != "services: {}" || !got.isDir || got.dir != "app" {
		t.Fatalf("app: %+v %v", got, err)
	}
	for _, p := range []string{"evil", "data", "data/secrets.age-key"} {
		if _, err := readCompose(r, p); !errors.Is(err, ErrPathOutsideRepo) {
			t.Errorf("%s: err = %v, want ErrPathOutsideRepo", p, err)
		}
	}
}

func TestCopyTreeSiblingsSkipsLinksOutOfRepo(t *testing.T) {
	clone := repo(t)
	dst := t.TempDir()
	if err := copyTreeSiblings(openRepo(t, clone), "app", dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "conf", "b.yml")); err != nil || string(b) != "a" {
		t.Errorf("in-repo symlink not copied as its target: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "key")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink to a server file was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "compose.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("compose.yaml copied as a sibling: %v", err)
	}
}

// A container with part of the stack dir bind-mounted can leave a symlink
// there; a later git sync must not write through it.
func TestCopyTreeSiblingsDoesNotFollowLinksInStackDir(t *testing.T) {
	clone := repo(t)
	dst := t.TempDir()
	victim := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(dst, "conf")); err != nil {
		t.Skip(err)
	}
	err := copyTreeSiblings(openRepo(t, clone), "app", dst)
	if err == nil {
		t.Error("sync wrote through a symlink in the stack dir")
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Errorf("files written outside the stack dir: %v", entries)
	}
}

// The clone cache sits under the data dir, which defaults to a relative
// path (./data).
func TestReadComposeWithRelativeCloneDir(t *testing.T) {
	clone := repo(t)
	t.Chdir(filepath.Dir(clone))
	if _, err := readCompose(openRepo(t, filepath.Base(clone)), "app"); err != nil {
		t.Fatalf("relative clone dir: %v", err)
	}
}

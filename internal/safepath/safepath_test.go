package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// layout builds root/{dir/file, dir/inner -> ../dir, escape -> <outside>,
// absescape -> /, dir/up -> ../../} next to an "outside" directory.
func layout(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "dir"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(root, "dir", "inner"): filepath.Join("..", "dir"),
		filepath.Join(root, "escape"):       outside,
		filepath.Join(root, "absescape"):    string(filepath.Separator),
		filepath.Join(root, "dir", "up"):    filepath.Join("..", ".."),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks not available: %v", err)
		}
	}
	return root
}

func TestResolve(t *testing.T) {
	root := layout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel  string
		want string // relative to root; "" = must fail with ErrEscape
	}{
		{"", "."},
		{"/", "."},
		{"dir/file", "dir/file"},
		{"/dir/file", "dir/file"},
		{"dir/../dir/file", "dir/file"},
		{"../../etc/passwd", "etc/passwd"}, // ".." can't climb above root
		{"dir/inner/file", "dir/file"},     // symlink that stays inside
		{"dir/new/deeper/file", "dir/new/deeper/file"},
		{"escape", ""},
		{"escape/secret", ""},
		{"escape/not-yet-there", ""},
		{"absescape/etc", ""},
		{"dir/up", ""},
		{"dir/up/outside/secret", ""},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, err := Resolve(root, tt.rel)
			if tt.want == "" {
				if !errors.Is(err, ErrEscape) {
					t.Fatalf("got %q, err %v; want ErrEscape", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(realRoot, filepath.FromSlash(tt.want)); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestResolveFollowsSymlinkedRoot(t *testing.T) {
	root := layout(t)
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	if _, err := Resolve(alias, "dir/file"); err != nil {
		t.Errorf("symlinked root: %v", err)
	}
	if _, err := Resolve(alias, "escape"); !errors.Is(err, ErrEscape) {
		t.Errorf("escape through symlinked root: %v", err)
	}
}

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/srv/root")
	tests := []struct {
		p    string
		want bool
	}{
		{"/srv/root", true},
		{"/srv/root/a/b", true},
		{"/srv/root-evil", false},
		{"/srv", false},
		{"/srv/root/../other", false},
		{"/srv/root/..data", true},
	}
	for _, tt := range tests {
		if got := Within(root, filepath.Clean(filepath.FromSlash(tt.p))); got != tt.want {
			t.Errorf("Within(%q) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

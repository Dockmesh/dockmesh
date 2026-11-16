//go:build unix

package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCheckout lays out top/src/data/dockmesh.db the way a source run
// leaves it and returns top/src.
func newCheckout(t *testing.T, top string) string {
	t.Helper()
	chmod(t, top, 0o750)
	dir := filepath.Join(top, "src")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "dockmesh.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTrustedCheckout(t *testing.T) {
	tests := []struct {
		name string
		// change breaks the layout below top; nil keeps it trusted.
		change  func(t *testing.T, top, dir string) string
		wantErr string
	}{
		{name: "own checkout"},
		{name: "group-writable checkout", change: func(t *testing.T, _, dir string) string { chmod(t, dir, 0o770); return dir }, wantErr: "writable"},
		{name: "world-writable data", change: func(t *testing.T, _, dir string) string { chmod(t, filepath.Join(dir, "data"), 0o757); return dir }, wantErr: "writable"},
		{name: "group-writable database", change: func(t *testing.T, _, dir string) string {
			chmod(t, filepath.Join(dir, "data", "dockmesh.db"), 0o620)
			return dir
		}, wantErr: "writable"},
		{name: "sticky world-writable parent", change: func(t *testing.T, top, dir string) string {
			chmod(t, top, 0o1777)
			return dir
		}, wantErr: "writable"},
		{name: "symlinked data", change: func(t *testing.T, top, dir string) string {
			real := filepath.Join(top, "elsewhere")
			if err := os.Rename(filepath.Join(dir, "data"), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(dir, "data")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, wantErr: "symlink"},
		{name: "symlinked database", change: func(t *testing.T, top, dir string) string {
			db := filepath.Join(dir, "data", "dockmesh.db")
			real := filepath.Join(top, "real.db")
			if err := os.Rename(db, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, db); err != nil {
				t.Fatal(err)
			}
			return dir
		}, wantErr: "file type"},
		{name: "hard-linked database", change: func(t *testing.T, top, dir string) string {
			if err := os.Link(filepath.Join(dir, "data", "dockmesh.db"), filepath.Join(top, "copy.db")); err != nil {
				t.Fatal(err)
			}
			return dir
		}, wantErr: "hard link"},
		{name: "checkout through a symlink", change: func(t *testing.T, top, dir string) string {
			link := filepath.Join(top, "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, wantErr: "symlink"},
		{name: "relative", change: func(*testing.T, string, string) string { return "src" }, wantErr: "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			top := t.TempDir()
			dir := newCheckout(t, top)
			if tt.change != nil {
				dir = tt.change(t, top, dir)
			}
			err := trustedCheckout(dir, os.Geteuid(), top)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// Owned by someone else: root's files pass, but this test cannot create
// a foreign file, so it checks the uid the process does not have.
func TestTrustedCheckoutOtherOwner(t *testing.T) {
	top := t.TempDir()
	dir := newCheckout(t, top)
	if err := trustedCheckout(dir, os.Geteuid()+1, top); os.Geteuid() != 0 && (err == nil || !strings.Contains(err.Error(), "owned by")) {
		t.Fatalf("err = %v, want owned by", err)
	}
}

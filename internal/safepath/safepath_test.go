package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

// The os package reports escapes only through the error text; this pins
// the text so a Go upgrade that changes it fails here, not silently in
// every caller.
func TestIsRootEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, err = r.Open("../x")
	if !IsRootEscape(err) {
		t.Fatalf(".. escape not recognised: %v", err)
	}
	if err := os.Symlink(base, filepath.Join(root, "up")); err == nil {
		if _, err := r.Open("up/root"); !IsRootEscape(err) {
			t.Errorf("symlink escape not recognised: %v", err)
		}
	}
	if _, err := r.Open("missing"); IsRootEscape(err) {
		t.Errorf("not-found reported as escape: %v", err)
	}
	if IsRootEscape(nil) {
		t.Error("nil is not an escape")
	}
}

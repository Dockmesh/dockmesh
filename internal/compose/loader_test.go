package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// compose-go never returns on an anchor that merges itself; the plain
// decode in front of it refuses the file.
func TestLoadProjectRefusesSelfMergingAnchor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("&a\n<<: *a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := LoadProject(ctx, dir, "loop", ""); err == nil || !strings.Contains(err.Error(), "invalid compose file") {
		t.Fatalf("err = %v", err)
	}
}

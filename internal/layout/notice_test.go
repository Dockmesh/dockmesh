package layout

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLegacyEnvFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the config only moves on Linux")
	}
	dir := t.TempDir()
	regular := filepath.Join(dir, "dockmesh.env")
	link := filepath.Join(dir, "link.env")
	config := filepath.Join(dir, "etc.env")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, env, want string }{
		{"unknown env file", "", ""},
		{"runs from the config", config, ""},
		{"old place, moved", link, ""},
		{"old place, not moved", regular, regular},
		{"missing file", filepath.Join(dir, "gone.env"), ""},
	}
	for _, tt := range tests {
		if got := LegacyEnvFile(tt.env, config); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

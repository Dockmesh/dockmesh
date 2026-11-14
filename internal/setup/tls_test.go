package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServesTLS(t *testing.T) {
	tests := []struct {
		name     string
		db       bool
		stateDir string // "dir", "file" or ""
		want     bool
	}{
		{name: "new install", want: true},
		{name: "new install restarted", db: true, stateDir: "dir", want: true},
		{name: "0.5.x unfinished install", db: true, want: false},
		{name: "state dir is a file", db: true, stateDir: "file", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := t.TempDir()
			switch tt.stateDir {
			case "dir":
				if _, err := EnsureStateDir(data); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(StateDir(data), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := ServesTLS(Facts{DB: tt.db}, data); got != tt.want {
				t.Errorf("ServesTLS = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureStateDirRefusesLink(t *testing.T) {
	data := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(data, StateDirName)); err != nil {
		t.Skip("no symlinks:", err)
	}
	if _, err := EnsureStateDir(data); err == nil {
		t.Error("a symlinked state dir was accepted")
	}
}

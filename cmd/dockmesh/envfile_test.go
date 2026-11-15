package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstExistingFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")
	if err := os.WriteFile(b, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"none", []string{a}, ""},
		{"second", []string{a, b}, b},
		{"empty list", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstExistingFile(tt.candidates); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultEnvFilesIncludeDataVolume(t *testing.T) {
	for _, p := range defaultEnvFiles {
		if p == "/data/dockmesh.env" {
			return
		}
	}
	t.Error("/data/dockmesh.env missing from defaultEnvFiles")
}

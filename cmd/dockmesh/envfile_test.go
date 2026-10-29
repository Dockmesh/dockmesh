package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
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

func TestWriteEnvFileIsReadByConfigLoad(t *testing.T) {
	dir := t.TempDir()
	want := &config.Config{
		HTTPAddr:         ":9191",
		BaseURL:          "https://dm.example.com",
		DBPath:           filepath.Join(dir, "data", "dockmesh.db"),
		StacksRoot:       filepath.Join(dir, "stacks"),
		SecretsPath:      filepath.Join(dir, "data", "secrets.env"),
		SecretsKeyPath:   filepath.Join(dir, "data", "secrets.age-key"),
		AuditGenesisPath: filepath.Join(dir, "data", "audit-genesis.sha256"),
	}
	if err := os.MkdirAll(filepath.Dir(want.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(dir, want); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "dockmesh.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			t.Setenv(k, v)
		}
	}

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, got, want string }{
		{"listen", got.HTTPAddr, want.HTTPAddr},
		{"base url", got.BaseURL, want.BaseURL},
		{"db path", got.DBPath, want.DBPath},
		{"stacks root", got.StacksRoot, want.StacksRoot},
		{"secrets path", got.SecretsPath, want.SecretsPath},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

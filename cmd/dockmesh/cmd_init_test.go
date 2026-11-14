package main

import (
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
)

func TestInitDBAndAdmin(t *testing.T) {
	cfg := &config.Config{DBPath: filepath.Join(t.TempDir(), "dockmesh.db")}
	created, err := initDBAndAdmin(cfg, "admin", "a-long-password-1")
	if err != nil || !created {
		t.Fatalf("first run: created=%v err=%v", created, err)
	}
	created, err = initDBAndAdmin(cfg, "admin", "another-password-2")
	if err != nil || created {
		t.Fatalf("re-run: created=%v err=%v", created, err)
	}
}

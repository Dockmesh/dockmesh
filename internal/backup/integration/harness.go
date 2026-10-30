//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
)

// NewService returns a backup service backed by a throwaway database.
// Only system sources are used, which need no volumes or stacks: the
// archive is a real tar.gz of the throwaway database.
func NewService(t testing.TB) (*backup.Service, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dockmesh.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	stacks := filepath.Join(dir, "stacks")
	if err := os.MkdirAll(stacks, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := backup.SystemPaths{DBPath: dbPath, StacksRoot: stacks, DataDir: dir}
	dc, err := docker.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	return backup.NewService(database, dc, nil, nil, paths), database
}

// RunID returns a short random id, so every run writes below its own path.
func RunID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "it-" + hex.EncodeToString(b)
}

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	dmdb "github.com/dockmesh/dockmesh/internal/db"
)

func TestTarSystemSkipsPremigrateSnapshots(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dockmesh.db")
	database, err := dmdb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, name := range []string{
		"secrets.env",
		"dockmesh.db.pre-migrate-20261029T101500.000Z.bak",
		"dockmesh.db.pre-migrate-20261029T101500.000Z.bak.tmp",
		"dockmesh.db.pre-migrate-manual.bak",
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	paths := SystemPaths{DBPath: dbPath, StacksRoot: t.TempDir(), DataDir: dataDir}
	if _, err := tarSystem(context.Background(), database, paths, &buf); err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = true
	}

	for name, want := range map[string]bool{
		"dockmesh.db":      true,
		"data/secrets.env": true,
		"data/dockmesh.db.pre-migrate-manual.bak":                   true,
		"data/dockmesh.db":                                          false,
		"data/dockmesh.db.pre-migrate-20261029T101500.000Z.bak":     false,
		"data/dockmesh.db.pre-migrate-20261029T101500.000Z.bak.tmp": false,
	} {
		if got[name] != want {
			t.Errorf("archive has %s = %v, want %v", name, got[name], want)
		}
	}
}

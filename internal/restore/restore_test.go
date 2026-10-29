package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
)

func TestExtractSkipsPremigrateSnapshots(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{
		"dockmesh.db",
		"data/secrets.env",
		"data/dockmesh.db.pre-migrate-20261029T101500.000Z.bak",
		"data/other.db.pre-migrate-20261029T101500.000Z.bak.tmp",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := &config.Config{DBPath: filepath.Join(dir, "dockmesh.db"), StacksRoot: filepath.Join(dir, "stacks")}
	if _, err := Extract(context.Background(), &buf, cfg, Options{Force: true}); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]bool{
		"dockmesh.db": true,
		"secrets.env": true,
		"dockmesh.db.pre-migrate-20261029T101500.000Z.bak":  false,
		"other.db.pre-migrate-20261029T101500.000Z.bak.tmp": false,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}

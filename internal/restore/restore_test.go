package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
)

type entry struct {
	name, body, link string
	typ              byte
}

func archive(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func restoreEnv(t *testing.T) (*config.Config, string) {
	t.Helper()
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DBPath:     filepath.Join(base, "data", "dockmesh.db"),
		StacksRoot: filepath.Join(base, "stacks"),
	}
	for _, d := range []string{filepath.Dir(cfg.DBPath), cfg.StacksRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, outside
}

func outsideIsEmpty(t *testing.T, outside string) {
	t.Helper()
	des, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 0 {
		t.Errorf("restore wrote outside its roots: %v", des)
	}
}

func TestExtractSkipsLinksAndStaysInside(t *testing.T) {
	cfg, outside := restoreEnv(t)
	a := archive(t,
		entry{name: "dockmesh.db", body: "db"},
		entry{name: "stacks/web/compose.yaml", body: "services: {}"},
		entry{name: "stacks/evil", typ: tar.TypeSymlink, link: outside},
		entry{name: "stacks/evil/payload", body: "x"},
		entry{name: "data/hard", typ: tar.TypeLink, link: "/etc/passwd"},
		entry{name: "data/secrets.env", body: "K=V"},
	)
	c, err := Extract(context.Background(), a, cfg, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.SkippedLinks != 2 {
		t.Errorf("skipped links = %d, want 2", c.SkippedLinks)
	}
	got, err := os.ReadFile(filepath.Join(cfg.StacksRoot, "web", "compose.yaml"))
	if err != nil || string(got) != "services: {}" {
		t.Errorf("regular stack file: %q %v", got, err)
	}
	if fi, err := os.Lstat(filepath.Join(cfg.StacksRoot, "evil")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("stacks/evil should be a plain dir now: %v %v", fi, err)
	}
	outsideIsEmpty(t, outside)
}

// A symlink already present under the stacks root must not redirect a
// restored file onto the host.
func TestExtractRefusesExistingSymlinkEscape(t *testing.T) {
	cfg, outside := restoreEnv(t)
	if err := os.Symlink(outside, filepath.Join(cfg.StacksRoot, "pre")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	a := archive(t,
		entry{name: "dockmesh.db", body: "db"},
		entry{name: "stacks/pre/cron", body: "* * * * * root id"},
	)
	if _, err := Extract(context.Background(), a, cfg, Options{Force: true}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("err = %v, want an escape error", err)
	}
	outsideIsEmpty(t, outside)
}

func TestExtractNeverWritesLiveDBFromData(t *testing.T) {
	cfg, _ := restoreEnv(t)
	a := archive(t,
		entry{name: "dockmesh.db", body: "snapshot"},
		entry{name: "data/dockmesh.db", body: "raw copy"},
		entry{name: "data/dockmesh.db-wal", body: "wal"},
	)
	if _, err := Extract(context.Background(), a, cfg, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfg.DBPath)
	if err != nil || string(got) != "snapshot" {
		t.Errorf("live DB = %q, %v; want the top-level snapshot", got, err)
	}
	if _, err := os.Stat(cfg.DBPath + "-wal"); err == nil {
		t.Error("WAL from data/ written next to the live DB")
	}
}

func TestExtractRejectsDotDot(t *testing.T) {
	cfg, outside := restoreEnv(t)
	a := archive(t,
		entry{name: "dockmesh.db", body: "db"},
		entry{name: "stacks/../../outside/x", body: "x"},
	)
	if _, err := Extract(context.Background(), a, cfg, Options{Force: true}); err == nil {
		t.Fatal("archive entry with .. accepted")
	}
	outsideIsEmpty(t, outside)
}

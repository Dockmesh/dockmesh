package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// ownerArchive builds a stack archive whose manifest records owner (nil
// for an archive without one), with a database directory owned by uid 999.
func ownerArchive(t *testing.T, owner *int) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest, err := json.Marshal(stackManifest{Format: 1, Stack: "db", OwnerUID: owner})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		hdr  tar.Header
		body string
	}{
		{tar.Header{Name: stackManifestName, Typeflag: tar.TypeReg, Mode: 0o600}, string(manifest)},
		{tar.Header{Name: "stack/compose.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 0}, "services: {}\n"},
		{tar.Header{Name: "stack/pgdata", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 999}, ""},
		{tar.Header{Name: "stack/pgdata/PG_VERSION", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 999}, "17\n"},
	} {
		e.hdr.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
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

func TestRestoreStackOwnersWithoutDocker(t *testing.T) {
	self := os.Getuid()
	tests := []struct {
		name     string
		owner    *int
		wantWarn bool
	}{
		{"archive without owners", nil, false},
		{"archive with owners", &self, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			m, err := stacks.NewManager(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { m.Close() })
			s := &Service{stacks: m}
			report := &RestoreReport{}
			if err := s.restoreStackInto(context.Background(), "db", ownerArchive(t, tt.owner), nil, nil, nil, false, report); err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(filepath.Join(root, "db", "pgdata", "PG_VERSION")); err != nil || string(b) != "17\n" {
				t.Fatalf("PG_VERSION = %q, %v", b, err)
			}
			warned := len(report.Warnings) == 1 && strings.Contains(report.Warnings[0], "2 entries of other owners")
			if warned != tt.wantWarn {
				t.Errorf("warnings = %v, want owner warning %v", report.Warnings, tt.wantWarn)
			}
		})
	}
}

func TestStackArchiveRecordsOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := tarStackWithVolumes(context.Background(), nil, nil, dir, "web", &buf); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	m, err := readStackManifest(tr)
	if err != nil {
		t.Fatal(err)
	}
	if m.OwnerUID == nil || *m.OwnerUID != os.Getuid() {
		t.Fatalf("owner_uid = %v, want %d", m.OwnerUID, os.Getuid())
	}
	if m.OwnerGID == nil || *m.OwnerGID != os.Getgid() {
		t.Fatalf("owner_gid = %v, want %d", m.OwnerGID, os.Getgid())
	}
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "stack/compose.yaml" || hdr.Uid != os.Getuid() {
		t.Fatalf("first stack entry = %+v, %v", hdr, err)
	}
}

func TestManagerFile(t *testing.T) {
	for rel, want := range map[string]bool{
		"compose.yaml": true, ".env": true, ".env.age": true, stacks.MetaFileName: true,
		"compose.prod.yaml": true, "compose.override.yml": true,
		"pgdata": false, "data/compose.yaml": false, "composer.json": false,
	} {
		if got := stacks.ManagerFile(rel); got != want {
			t.Errorf("stacks.ManagerFile(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestForeignComparesUidAndGid(t *testing.T) {
	uid, gid := 999, 999
	withGID := &stackManifest{OwnerUID: &uid, OwnerGID: &gid}
	uidOnly := &stackManifest{OwnerUID: &uid}
	tests := []struct {
		name     string
		m        *stackManifest
		uid, gid int
		want     bool
	}{
		{"server's own", withGID, 999, 999, false},
		{"postgres-alpine data", withGID, 70, 70, true},
		{"server's uid, other group", withGID, 999, 1000, true},
		{"old archive without gid", uidOnly, 999, 1000, false},
		{"old archive, other uid", uidOnly, 70, 70, true},
		{"no manifest", nil, 70, 70, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := foreign(tt.m, &tar.Header{Uid: tt.uid, Gid: tt.gid}); got != tt.want {
				t.Errorf("foreign = %v, want %v", got, tt.want)
			}
		})
	}
}

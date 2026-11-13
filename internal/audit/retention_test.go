package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

type mapSettings map[string]string

func (m mapSettings) Get(key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func TestDefaultLocalDir(t *testing.T) {
	tests := []struct {
		name       string
		legacyDir  bool
		wantLegacy bool
	}{
		{"fresh install uses the data dir", false, false},
		{"old working-directory archive is kept", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := t.TempDir()
			t.Chdir(wd)
			if tt.legacyDir {
				if err := os.MkdirAll(filepath.Join(wd, "data", "audit-archive"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			dataDir := filepath.Join(t.TempDir(), "dockmesh")
			r := NewRetention(nil, nil, mapSettings{}, nil, dataDir)
			want := filepath.Join(dataDir, "audit-archive")
			if tt.wantLegacy {
				want = filepath.Join(wd, "data", "audit-archive")
			}
			if got := r.DefaultLocalDir(); got != want {
				t.Errorf("DefaultLocalDir() = %q, want %q", got, want)
			}
		})
	}
}

func TestArchiveLocalWithoutFolderWritesToDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	svc := NewService(database, filepath.Join(t.TempDir(), "genesis"))
	ctx := context.Background()
	svc.Write(ctx, "", ActionLogin, "t", nil)
	if _, err := database.Exec(`UPDATE audit_log SET ts = ?`, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	r := NewRetention(database, svc, mapSettings{
		RetentionModeKey: "archive_local",
		RetentionDaysKey: "7",
	}, nil, dataDir)
	res, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Archived || !strings.HasPrefix(res.ArchivePath, filepath.Join(dataDir, "audit-archive")+string(filepath.Separator)) {
		t.Fatalf("archive path = %q (archived %v), want below %s", res.ArchivePath, res.Archived, dataDir)
	}
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Errorf("archive file: %v", err)
	}
	if _, err := os.Stat("data"); !os.IsNotExist(err) {
		t.Errorf("relative ./data was created: %v", err)
	}
}

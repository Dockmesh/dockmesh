package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// writeTestSnapshot copies the live database to a snapshot file and runs
// edit on the copy.
func writeTestSnapshot(t *testing.T, database *sql.DB, path string, edit ...string) {
	t.Helper()
	if _, err := database.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	snap, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	for _, q := range edit {
		if _, err := snap.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func openNotices(t *testing.T, n *notices.Service) map[string]notices.Notice {
	t.Helper()
	list, err := n.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]notices.Notice{}
	for _, it := range list {
		out[it.Key] = it
	}
	return out
}

func TestRemovePlaintextSnapshots(t *testing.T) {
	const (
		preSeal  = `DELETE FROM schema_migrations WHERE version LIKE '20261102125620%'`
		plainCfg = `UPDATE backup_targets SET config_json = '{"bucket":"b","secret_key":"plain"}'`
	)
	tests := []struct {
		name        string
		encrypt     bool
		snapEdit    []string
		liveEdit    string
		unfinished  bool
		wantRemoved bool
		wantKept    bool
	}{
		{"upgrade from before the encryption", true, []string{preSeal, plainCfg}, "", false, true, false},
		{"snapshot with a plaintext credential", true, []string{plainCfg}, "", false, true, false},
		{"sealed snapshot stays as restore point", true, nil, "", false, false, false},
		{"unfinished copy is removed", true, nil, "", true, true, false},
		{"failed seal check keeps it", true, []string{preSeal}, plainCfg, false, false, true},
		{"encryption off leaves it alone", false, []string{preSeal}, "", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "dockmesh.db")
			database, err := dbtest.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			sec, err := secrets.New(filepath.Join(dir, "key"), tt.encrypt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := targets.NewTargetStore(database, sec).Create(ctx, targets.TargetInput{Name: "s3", Type: "s3",
				Config: map[string]any{"endpoint": "e", "bucket": "b", "access_key": "a", "secret_key": "s3-secret"}}); err != nil {
				t.Fatal(err)
			}
			snap := dbPath + ".pre-migrate-20261103T091500.000Z.bak"
			if tt.unfinished {
				snap += ".tmp"
			}
			writeTestSnapshot(t, database, snap, tt.snapEdit...)
			if tt.liveEdit != "" {
				if _, err := database.Exec(tt.liveEdit); err != nil {
					t.Fatal(err)
				}
			}
			n := notices.NewService(database)

			if err := removePlaintextSnapshots(ctx, database, sec, n); err != nil {
				t.Fatal(err)
			}

			_, statErr := os.Stat(snap)
			if gone := os.IsNotExist(statErr); gone != tt.wantRemoved {
				t.Errorf("snapshot gone = %v, want %v", gone, tt.wantRemoved)
			}
			open := openNotices(t, n)
			if _, ok := open[snapshotRemovedNoticeKey]; ok != tt.wantRemoved {
				t.Errorf("removed notice = %v, want %v", ok, tt.wantRemoved)
			}
			if _, ok := open[snapshotKeptNoticeKey]; ok != tt.wantKept {
				t.Errorf("kept notice = %v, want %v", ok, tt.wantKept)
			}
			if r, ok := open[snapshotRemovedNoticeKey]; ok && r.Severity != notices.SevInfo {
				t.Errorf("removed notice severity %q", r.Severity)
			}
		})
	}
}

// Once the admin deletes a kept snapshot, the next start resolves the
// notice.
func TestRemovePlaintextSnapshotsResolves(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dockmesh.db")
	database, err := dbtest.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.New(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	n := notices.NewService(database)
	if err := n.Raise(ctx, keptNotice([]string{"x"}, i18n.Msg("Test."))); err != nil {
		t.Fatal(err)
	}
	if err := removePlaintextSnapshots(ctx, database, sec, n); err != nil {
		t.Fatal(err)
	}
	if _, ok := openNotices(t, n)[snapshotKeptNoticeKey]; ok {
		t.Error("kept notice still open without snapshots")
	}
}

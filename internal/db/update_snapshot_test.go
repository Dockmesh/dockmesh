package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openWAL(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestUpdateSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dockmesh.db")
	d := openWAL(t, path)
	if _, err := d.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('before')`); err != nil {
		t.Fatal(err)
	}
	// Close without a checkpoint is not possible from database/sql, so the
	// WAL may be folded in; the snapshot must hold the row either way.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := WriteUpdateSnapshot(path); err != nil {
		t.Fatal(err)
	}

	d = openWAL(t, path)
	if _, err := d.Exec(`INSERT INTO t VALUES ('after'); CREATE TABLE migrated (x INT)`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := RestoreUpdateSnapshot(path); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + s); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left after restore", s)
		}
	}
	d = openWAL(t, path)
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after restore: %d, %v", n, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'migrated'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("table of the new version survived the restore: %d, %v", n, err)
	}
	if _, err := os.Stat(UpdateSnapshotPath(path)); err != nil {
		t.Errorf("snapshot gone after restore: %v", err)
	}
}

func TestUpdateSnapshotWithoutDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dockmesh.db")
	if err := os.WriteFile(UpdateSnapshotPath(path), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteUpdateSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(UpdateSnapshotPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale snapshot kept although there is no database")
	}
	if err := os.WriteFile(path, []byte("written by the new version"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreUpdateSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("database of the new version still in place")
	}
	if _, err := os.Stat(path + ".after-failed-update"); err != nil {
		t.Fatalf("database of the new version not kept aside: %v", err)
	}
}

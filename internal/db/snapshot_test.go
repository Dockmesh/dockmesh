package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const snapshotSeedVersion = "058_notices.sql"

func snapshotFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.pre-migrate-*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range matches {
		out = append(out, filepath.Base(m))
	}
	sort.Strings(out)
	return out
}

func countApplied(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fixClock(t *testing.T, ts time.Time) {
	t.Helper()
	prev := nowUTC
	nowUTC = func() time.Time { return ts }
	t.Cleanup(func() { nowUTC = prev })
}

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotBeforeMigrate(t *testing.T) {
	ts := time.Date(2026, 10, 29, 10, 15, 0, 0, time.UTC)
	const (
		wantName = "test.db.pre-migrate-20261029T101500.000Z.bak"
		nextName = "test.db.pre-migrate-20261029T101500.001Z.bak"
	)
	seedOld := func(t *testing.T, database *sql.DB) {
		if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name string
		// seed brings the database into its pre-Migrate state.
		seed func(t *testing.T, database *sql.DB)
		// prepare runs against the DB directory before Migrate.
		prepare      func(t *testing.T, dir string, database *sql.DB)
		env          string
		wantErr      bool
		wantSnaps    []string
		checkPayload string
	}{
		{
			name: "fresh database",
			seed: func(t *testing.T, database *sql.DB) {},
		},
		{
			name: "up to date",
			seed: func(t *testing.T, database *sql.DB) {
				if err := Migrate(database); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:         "pending migrations",
			seed:         seedOld,
			wantSnaps:    []string{wantName},
			checkPayload: wantName,
		},
		{
			name: "rotation keeps newest three",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, _ *sql.DB) {
				writeFiles(t, dir,
					"test.db.pre-migrate-20250101T000000.000Z.bak",
					"test.db.pre-migrate-20250201T000000.000Z.bak",
					"test.db.pre-migrate-20250301T000000.000Z.bak",
					"test.db.pre-migrate-20250401T000000.000Z.bak",
					"other.db.pre-migrate-20240101T000000.000Z.bak",
					"test.db.pre-migrate-manual.bak",
				)
			},
			wantSnaps: []string{
				"other.db.pre-migrate-20240101T000000.000Z.bak",
				"test.db.pre-migrate-20250301T000000.000Z.bak",
				"test.db.pre-migrate-20250401T000000.000Z.bak",
				wantName,
				"test.db.pre-migrate-manual.bak",
			},
		},
		{
			name: "new snapshot is never pruned",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, _ *sql.DB) {
				writeFiles(t, dir,
					"test.db.pre-migrate-20270101T000000.000Z.bak",
					"test.db.pre-migrate-20270201T000000.000Z.bak",
					"test.db.pre-migrate-20270301T000000.000Z.bak",
				)
			},
			wantSnaps: []string{
				wantName,
				"test.db.pre-migrate-20270201T000000.000Z.bak",
				"test.db.pre-migrate-20270301T000000.000Z.bak",
			},
			checkPayload: wantName,
		},
		{
			name: "same name already taken",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, _ *sql.DB) {
				writeFiles(t, dir, wantName)
			},
			wantSnaps:    []string{wantName, nextName},
			checkPayload: nextName,
		},
		{
			name: "stale temp file is replaced",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, _ *sql.DB) {
				writeFiles(t, dir, wantName+".tmp")
			},
			wantSnaps:    []string{wantName},
			checkPayload: wantName,
		},
		{
			name: "existing snapshot at same version is reused",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, database *sql.DB) {
				old := filepath.Join(dir, "test.db.pre-migrate-20261028T000000.000Z.bak")
				if _, err := database.Exec(`VACUUM INTO ?`, old); err != nil {
					t.Fatal(err)
				}
			},
			wantSnaps: []string{"test.db.pre-migrate-20261028T000000.000Z.bak"},
		},
		{
			name: "existing snapshot at older version is not reused",
			seed: func(t *testing.T, database *sql.DB) {
				if err := migrateUpTo(database, "057_notifications_per_user.sql"); err != nil {
					t.Fatal(err)
				}
			},
			prepare: func(t *testing.T, dir string, database *sql.DB) {
				old := filepath.Join(dir, "test.db.pre-migrate-20261028T000000.000Z.bak")
				if _, err := database.Exec(`VACUUM INTO ?`, old); err != nil {
					t.Fatal(err)
				}
				if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
					t.Fatal(err)
				}
			},
			wantSnaps:    []string{"test.db.pre-migrate-20261028T000000.000Z.bak", wantName},
			checkPayload: wantName,
		},
		{
			name: "snapshot fails",
			seed: seedOld,
			prepare: func(t *testing.T, dir string, _ *sql.DB) {
				blocker := filepath.Join(dir, wantName+".tmp")
				if err := os.Mkdir(blocker, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFiles(t, blocker, "keep")
			},
			wantErr:   true,
			wantSnaps: []string{wantName + ".tmp"},
		},
		{
			name: "override skips snapshot",
			seed: seedOld,
			env:  "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixClock(t, ts)
			dir := t.TempDir()
			path := filepath.Join(dir, "test.db")
			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()

			// The seed's own migrations must not leave snapshots behind.
			t.Setenv(SkipSnapshotEnv, "1")
			tt.seed(t, database)
			if tt.prepare != nil {
				tt.prepare(t, dir, database)
			}
			t.Setenv(SkipSnapshotEnv, tt.env)
			var before int
			if tt.wantErr {
				before = countApplied(t, database)
			}

			err = Migrate(database)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Migrate succeeded, want error")
				}
				if !strings.Contains(err.Error(), SkipSnapshotEnv) {
					t.Errorf("error %q does not name %s", err, SkipSnapshotEnv)
				}
				if after := countApplied(t, database); after != before {
					t.Errorf("applied migrations went from %d to %d despite failed snapshot", before, after)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			if got := snapshotFiles(t, dir); strings.Join(got, ",") != strings.Join(tt.wantSnaps, ",") {
				t.Errorf("snapshots = %v, want %v", got, tt.wantSnaps)
			}
			if tt.checkPayload != "" {
				checkSnapshot(t, filepath.Join(dir, tt.checkPayload))
			}
		})
	}
}

func checkSnapshot(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("snapshot mode = %o, want 600", mode)
		}
	}
	snap, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var integrity string
	if err := snap.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q", integrity)
	}
	var latest string
	if err := snap.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if latest != snapshotSeedVersion {
		t.Errorf("snapshot schema at %s, want %s", latest, snapshotSeedVersion)
	}
}

func TestIsSnapshotName(t *testing.T) {
	tests := []struct {
		base, name string
		want       bool
		finished   bool
	}{
		{"dockmesh.db", "dockmesh.db.pre-migrate-20261029T101500.000Z.bak", true, true},
		{"dockmesh.db", "dockmesh.db.pre-migrate-20261029T101500.000Z.bak.tmp", true, false},
		{"", "other.db.pre-migrate-20261029T101500.000Z.bak", true, true},
		{"dockmesh.db", "other.db.pre-migrate-20261029T101500.000Z.bak", false, false},
		{"dockmesh.db", "dockmesh.db.pre-migrate-20261029T101500Z.bak", false, false},
		{"dockmesh.db", "dockmesh.db.pre-migrate-manual.bak", false, false},
		{"dockmesh.db", "dockmesh.db", false, false},
		{"", ".pre-migrate-20261029T101500.000Z.bak", false, false},
	}
	for _, tt := range tests {
		if got := IsSnapshotName(tt.base, tt.name); got != tt.want {
			t.Errorf("IsSnapshotName(%q, %q) = %v, want %v", tt.base, tt.name, got, tt.want)
		}
		if got := isFinishedSnapshot(tt.base, tt.name); got != tt.finished {
			t.Errorf("isFinishedSnapshot(%q, %q) = %v, want %v", tt.base, tt.name, got, tt.finished)
		}
	}
}

func TestSnapshotSkipsInMemory(t *testing.T) {
	database, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFiles(t, "."); len(got) != 0 {
		t.Errorf("in-memory database produced snapshots: %v", got)
	}
}

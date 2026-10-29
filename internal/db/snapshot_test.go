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

func listSnapshots(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.pre-migrate-*.bak"))
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

func TestSnapshotBeforeMigrate(t *testing.T) {
	ts := time.Date(2026, 10, 29, 10, 15, 0, 0, time.UTC)
	wantName := "test.db.pre-migrate-20261029T101500Z.bak"

	tests := []struct {
		name string
		// seed brings the database into its pre-Migrate state.
		seed func(t *testing.T, database *sql.DB)
		// prepare runs in the DB directory before Migrate.
		prepare      func(t *testing.T, dir string)
		env          string
		wantErr      bool
		wantSnaps    []string
		checkPayload bool
	}{
		{
			name:      "fresh database",
			seed:      func(t *testing.T, database *sql.DB) {},
			wantSnaps: nil,
		},
		{
			name: "up to date",
			seed: func(t *testing.T, database *sql.DB) {
				if err := Migrate(database); err != nil {
					t.Fatal(err)
				}
			},
			wantSnaps: nil,
		},
		{
			name: "pending migrations",
			seed: func(t *testing.T, database *sql.DB) {
				if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
					t.Fatal(err)
				}
			},
			wantSnaps:    []string{wantName},
			checkPayload: true,
		},
		{
			name: "rotation keeps newest three",
			seed: func(t *testing.T, database *sql.DB) {
				if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
					t.Fatal(err)
				}
			},
			prepare: func(t *testing.T, dir string) {
				for _, n := range []string{
					"test.db.pre-migrate-20250101T000000Z.bak",
					"test.db.pre-migrate-20250201T000000Z.bak",
					"test.db.pre-migrate-20250301T000000Z.bak",
					"test.db.pre-migrate-20250401T000000Z.bak",
					"other.db.pre-migrate-20240101T000000Z.bak",
					"test.db.pre-migrate-manual.bak",
				} {
					if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantSnaps: []string{
				"other.db.pre-migrate-20240101T000000Z.bak",
				"test.db.pre-migrate-20250301T000000Z.bak",
				"test.db.pre-migrate-20250401T000000Z.bak",
				wantName,
				"test.db.pre-migrate-manual.bak",
			},
		},
		{
			name: "snapshot fails",
			seed: func(t *testing.T, database *sql.DB) {
				if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
					t.Fatal(err)
				}
			},
			prepare: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, wantName), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr:   true,
			wantSnaps: []string{wantName},
		},
		{
			name: "override skips snapshot",
			seed: func(t *testing.T, database *sql.DB) {
				if err := migrateUpTo(database, snapshotSeedVersion); err != nil {
					t.Fatal(err)
				}
			},
			env:       "1",
			wantSnaps: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixClock(t, ts)
			t.Setenv(SkipSnapshotEnv, tt.env)
			dir := t.TempDir()
			path := filepath.Join(dir, "test.db")
			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()

			// The seed's own Migrate runs must not leave snapshots behind.
			t.Setenv(SkipSnapshotEnv, "1")
			tt.seed(t, database)
			t.Setenv(SkipSnapshotEnv, tt.env)
			if got := listSnapshots(t, dir); len(got) != 0 {
				t.Fatalf("seed left snapshots: %v", got)
			}
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}
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

			if got := listSnapshots(t, dir); strings.Join(got, ",") != strings.Join(tt.wantSnaps, ",") {
				t.Errorf("snapshots = %v, want %v", got, tt.wantSnaps)
			}
			if tt.checkPayload {
				checkSnapshot(t, filepath.Join(dir, wantName))
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
	if got := listSnapshots(t, "."); len(got) != 0 {
		t.Errorf("in-memory database produced snapshots: %v", got)
	}
}

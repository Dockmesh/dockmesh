package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// SkipSnapshotEnv lets an operator migrate without the pre-migration
// snapshot, e.g. when the disk is too full to hold a copy of the database.
const SkipSnapshotEnv = "DOCKMESH_SKIP_PREMIGRATE_BACKUP"

const keepSnapshots = 3

// nowUTC is swapped in tests to get a predictable snapshot name.
var nowUTC = func() time.Time { return time.Now().UTC() }

// snapshotBeforeMigrate writes <db>.pre-migrate-<UTC timestamp>.bak next to
// the database file and prunes older snapshots of the same database.
// In-memory databases have no file and are skipped.
func snapshotBeforeMigrate(db *sql.DB) error {
	if v := os.Getenv(SkipSnapshotEnv); v == "1" || v == "true" {
		slog.Warn("skipping pre-migration database snapshot", "env", SkipSnapshotEnv)
		return nil
	}
	path, err := mainDBFile(db)
	if err != nil {
		return snapshotError(err)
	}
	if path == "" {
		return nil
	}
	target := fmt.Sprintf("%s.pre-migrate-%s.bak", path, nowUTC().Format("20060102T150405Z"))
	if err := vacuumInto(db, path, target); err != nil {
		return snapshotError(err)
	}
	slog.Info("database snapshot written before migration", "path", target)
	pruneSnapshots(path)
	return nil
}

func snapshotError(err error) error {
	return fmt.Errorf("pre-migration database snapshot failed, no migration applied: %w "+
		"(free disk space next to the database or fix its directory permissions; "+
		"set %s=1 to migrate without a snapshot)", err, SkipSnapshotEnv)
}

func mainDBFile(db *sql.DB) (string, error) {
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return file, nil
		}
	}
	return "", rows.Err()
}

func vacuumInto(db *sql.DB, dbPath, target string) error {
	// Creating the file up front fixes its mode before any data lands in
	// it; VACUUM INTO accepts an existing empty file.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	if err := chownLike(target, dbPath); err != nil {
		_ = os.Remove(target)
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, target); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

func pruneSnapshots(dbPath string) {
	dir, base := filepath.Split(dbPath)
	if dir == "" {
		dir = "."
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.pre-migrate-\d{8}T\d{6}Z\.bak$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("listing database snapshots failed", "dir", dir, "err", err)
		return
	}
	var snaps []string
	for _, e := range entries {
		if e.Type().IsRegular() && pattern.MatchString(e.Name()) {
			snaps = append(snaps, e.Name())
		}
	}
	if len(snaps) <= keepSnapshots {
		return
	}
	sort.Strings(snaps)
	for _, name := range snaps[:len(snaps)-keepSnapshots] {
		p := filepath.Join(dir, name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing old database snapshot failed", "path", p, "err", err)
		}
	}
}

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
	"strings"
	"time"
)

// SkipSnapshotEnv lets an operator migrate without the pre-migration
// snapshot, e.g. when the disk is too full to hold a copy of the database.
const SkipSnapshotEnv = "DOCKMESH_SKIP_PREMIGRATE_BACKUP"

const (
	keepSnapshots  = 3
	snapshotLayout = "20060102T150405.000Z"
	tmpSuffix      = ".tmp"
	// Bounds the search for a free name when snapshots land in the same
	// millisecond.
	nameAttempts = 10
)

var snapshotSuffix = regexp.MustCompile(`\.pre-migrate-\d{8}T\d{6}\.\d{3}Z\.bak(\.tmp)?$`)

// nowUTC is swapped in tests to get a predictable snapshot name.
var nowUTC = func() time.Time { return time.Now().UTC() }

// IsSnapshotName reports whether name is a pre-migration snapshot (finished
// or in progress) of the database file named dbBase. An empty dbBase
// matches snapshots of any database.
func IsSnapshotName(dbBase, name string) bool {
	loc := snapshotSuffix.FindStringIndex(name)
	if loc == nil || loc[0] == 0 {
		return false
	}
	return dbBase == "" || name[:loc[0]] == dbBase
}

func isFinishedSnapshot(dbBase, name string) bool {
	return IsSnapshotName(dbBase, name) && !strings.HasSuffix(name, tmpSuffix)
}

// snapshotBeforeMigrate writes <db>.pre-migrate-<UTC timestamp>.bak next to
// the database file and prunes older snapshots of the same database.
// In-memory databases have no file and are skipped. state is the
// migrationState of db; if the newest snapshot is already in that state it
// is reused, so a migration that fails on every start does not rotate the
// pre-upgrade copy out.
func snapshotBeforeMigrate(db *sql.DB, state string) error {
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
	existing, err := listSnapshots(path)
	if err != nil {
		return snapshotError(err)
	}
	if n := len(existing); n > 0 && snapshotState(existing[n-1]) == state {
		slog.Info("reusing existing database snapshot from before migration", "path", existing[n-1])
		return nil
	}
	target, err := writeSnapshot(db, path)
	if err != nil {
		return snapshotError(err)
	}
	slog.Info("database snapshot written before migration", "path", target)
	pruneSnapshots(path, target)
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

// listSnapshots returns the finished snapshots of dbPath, oldest first.
func listSnapshots(dbPath string) ([]string, error) {
	dir, base := filepath.Split(dbPath)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var snaps []string
	for _, e := range entries {
		if e.Type().IsRegular() && isFinishedSnapshot(base, e.Name()) {
			snaps = append(snaps, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(snaps)
	return snaps, nil
}

// snapshotState returns the migrationState of the snapshot at path, or ""
// when it cannot be read.
func snapshotState(path string) string {
	snap, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return ""
	}
	defer snap.Close()
	state, err := migrationState(snap)
	if err != nil {
		return ""
	}
	return state
}

// writeSnapshot fills <target>.tmp and renames it into place, so a crash
// mid-copy never leaves a file that looks like a finished snapshot.
func writeSnapshot(db *sql.DB, dbPath string) (string, error) {
	ts := nowUTC()
	var target string
	for i := 0; ; i++ {
		if i == nameAttempts {
			return "", fmt.Errorf("no free snapshot name after %d attempts", nameAttempts)
		}
		target = fmt.Sprintf("%s.pre-migrate-%s.bak", dbPath, ts.Add(time.Duration(i)*time.Millisecond).Format(snapshotLayout))
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
	}
	tmp := target + tmpSuffix
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Creating the file up front fixes its mode and owner before any data
	// lands in it; VACUUM INTO accepts an existing empty file.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // derived from the configured DB path
	if err != nil {
		return "", err
	}
	if err := chownLike(f, dbPath); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return target, nil
}

// pruneSnapshots deletes the oldest snapshots of dbPath so keepSnapshots
// remain, never touching keep (the one just written).
func pruneSnapshots(dbPath, keep string) {
	snaps, err := listSnapshots(dbPath)
	if err != nil {
		slog.Warn("listing database snapshots failed", "db", dbPath, "err", err)
		return
	}
	var others []string
	for _, s := range snaps {
		if s != keep {
			others = append(others, s)
		}
	}
	if len(others) < keepSnapshots {
		return
	}
	for _, p := range others[:len(others)-(keepSnapshots-1)] {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing old database snapshot failed", "path", p, "err", err)
		}
	}
}

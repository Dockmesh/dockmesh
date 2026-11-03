package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
// migrationState of db. When the newest snapshot is in that state and holds
// the same data it is reused, so a migration that fails on every start does
// not rotate the pre-upgrade copy out. Same state alone is not enough: a
// backup restored in that state has its own data and needs its own copy.
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
	target, err := writeSnapshot(db, path)
	if err != nil {
		return snapshotError(err)
	}
	if n := len(existing); n > 0 && snapshotState(existing[n-1]) == state {
		same, err := sameContent(existing[n-1], target)
		if err != nil {
			slog.Warn("comparing database snapshots failed, keeping both", "path", existing[n-1], "err", err)
		}
		if same {
			if err := os.Remove(target); err != nil {
				return snapshotError(err)
			}
			slog.Info("reusing existing database snapshot from before migration", "path", existing[n-1])
			return nil
		}
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
func listSnapshots(dbPath string) ([]string, error) { return listMatching(dbPath, isFinishedSnapshot) }

// listMatching returns the files next to dbPath whose names match, sorted.
func listMatching(dbPath string, match func(dbBase, name string) bool) ([]string, error) {
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
		if e.Type().IsRegular() && match(base, e.Name()) {
			snaps = append(snaps, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(snaps)
	return snaps, nil
}

// Snapshots returns the pre-migration snapshots of db's file, finished
// and unfinished, oldest first.
func Snapshots(db *sql.DB) ([]string, error) {
	path, err := mainDBFile(db)
	if err != nil || path == "" {
		return nil, err
	}
	return listMatching(path, IsSnapshotName)
}

// IsUnfinishedSnapshot reports whether path is a snapshot whose copy never
// completed.
func IsUnfinishedSnapshot(path string) bool { return strings.HasSuffix(path, tmpSuffix) }

// OpenSnapshot opens a snapshot file read-only.
func OpenSnapshot(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro")
}

// snapshotState returns the migrationState of the snapshot at path, or ""
// when it cannot be read.
func snapshotState(path string) string {
	snap, err := OpenSnapshot(path)
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

// sameContent reports whether the files at a and b are byte-identical.
// VACUUM INTO writes the same bytes for the same data, so equal snapshots
// mean the database did not change in between.
func sameContent(a, b string) (bool, error) {
	fa, err := os.Open(a) //nolint:gosec // snapshot paths derive from the configured DB path
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b) //nolint:gosec // snapshot paths derive from the configured DB path
	if err != nil {
		return false, err
	}
	defer fb.Close()
	sa, err := fa.Stat()
	if err != nil {
		return false, err
	}
	sb, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if sa.Size() != sb.Size() {
		return false, nil
	}
	bufA := make([]byte, 64<<10)
	bufB := make([]byte, 64<<10)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		doneA := errors.Is(errA, io.EOF) || errors.Is(errA, io.ErrUnexpectedEOF)
		doneB := errors.Is(errB, io.EOF) || errors.Is(errB, io.ErrUnexpectedEOF)
		if errA != nil && !doneA {
			return false, errA
		}
		if errB != nil && !doneB {
			return false, errB
		}
		if doneA || doneB {
			return doneA == doneB, nil
		}
	}
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

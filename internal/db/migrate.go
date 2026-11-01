package db

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrationFS returns the migrations shipped with the binary.
func migrationFS() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// New migrations are named <UTC timestamp>_<slug>.sql. The numbered names
// predate that scheme and are frozen: they keep their identity in
// schema_migrations, which records the full file name, so an applied
// migration file must never be renamed.
var (
	numberedName    = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)
	timestampedName = regexp.MustCompile(`^(\d{14})_[a-z0-9_]+\.sql$`)
)

const timestampLayout = "20060102150405"

type migration struct {
	name    string
	version string
}

func parseMigrationName(name string) (migration, error) {
	if m := numberedName.FindStringSubmatch(name); m != nil {
		return migration{name: name, version: m[1]}, nil
	}
	if m := timestampedName.FindStringSubmatch(name); m != nil {
		if _, err := time.Parse(timestampLayout, m[1]); err != nil {
			return migration{}, fmt.Errorf("migration %s: %s is not a valid timestamp", name, m[1])
		}
		return migration{name: name, version: m[1]}, nil
	}
	return migration{}, fmt.Errorf("migration %s: name must be YYYYMMDDHHMMSS_<slug>.sql (UTC, lower-case slug)", name)
}

// loadMigrations returns the migrations in fsys in apply order: the
// numbered ones by number, then the timestamped ones by timestamp.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	byVersion := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if other, ok := byVersion[m.version]; ok {
			return nil, fmt.Errorf("migrations %s and %s share version %s", other, m.name, m.version)
		}
		byVersion[m.version] = m.name
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b migration) int {
		if len(a.version) != len(b.version) {
			return len(a.version) - len(b.version)
		}
		return strings.Compare(a.version, b.version)
	})
	return out, nil
}

// Migrate applies all pending migrations. On an existing database it first
// writes a snapshot next to the DB file (see snapshotBeforeMigrate).
//
// Every migration that is not recorded as applied runs, in version order,
// even when a newer one was applied before it: branches developed in
// parallel can merge in any order. Migrations of one release therefore must
// not depend on each other's order.
func Migrate(db *sql.DB) error { return migrate(db, migrationFS(), "") }

// migrateUpTo applies migrations up to and including last ("" = all), so
// tests can seed data in an older schema before running the rest.
func migrateUpTo(db *sql.DB, last string) error { return migrate(db, migrationFS(), last) }

func migrate(db *sql.DB, fsys fs.FS, last string) error {
	all, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if last != "" {
		i := slices.IndexFunc(all, func(m migration) bool { return m.name == last })
		if i < 0 {
			return fmt.Errorf("migration %s not found", last)
		}
		all = all[:i+1]
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	var pending []migration
	for _, m := range all {
		if !applied[m.name] {
			pending = append(pending, m)
		}
	}
	// A database without any applied migration is a fresh install with
	// nothing in it to lose.
	if len(pending) > 0 && len(applied) > 0 {
		state, err := migrationState(db)
		if err != nil {
			return err
		}
		if err := snapshotBeforeMigrate(db, state); err != nil {
			return err
		}
	}

	for _, m := range pending {
		if err := applyMigration(db, fsys, m.name); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and records it in the same
// transaction, so a failure leaves it unrecorded and the next start
// retries it.
func applyMigration(db *sql.DB, fsys fs.FS, name string) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(string(b)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func appliedMigrations(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrationState identifies the set of applied migrations. The newest
// version alone is not enough: a migration with an older timestamp can be
// applied after a newer one.
func migrationState(db *sql.DB) (string, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		h.Write([]byte(v + "\n"))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

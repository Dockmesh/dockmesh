package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens the Dockmesh DB. The returned *sql.DB is wrapped by
// otelsql so every QueryContext / ExecContext becomes a `db.query`
// span under whatever parent trace is on the context. When tracing
// is disabled (no OTLP endpoint configured) the wrapper is a cheap
// passthrough — spans have no exporter, so they're dropped.
//
// P.12.3.1 — deep tracing.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// busy_timeout comes first: journal_mode(WAL) takes a lock, and a new
	// pool connection opened while another one writes would fail with
	// SQLITE_BUSY before the timeout applied.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := otelsql.Open("sqlite", dsn, otelsql.WithAttributes(
		semconv.DBSystemSqlite,
	), otelsql.WithSpanOptions(otelsql.SpanOptions{
		Ping:                 false, // readiness probe fires this every few seconds; too noisy
		DisableErrSkip:       true,
		OmitConnectorConnect: true,
		OmitConnResetSession: true,
		OmitConnPrepare:      true,
		OmitRows:             true,
	}))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// Migrate applies all pending migrations. On an existing database it first
// writes a snapshot next to the DB file (see snapshotBeforeMigrate).
func Migrate(db *sql.DB) error { return migrateUpTo(db, "") }

// migrateUpTo applies migrations up to and including last ("" = all), so
// tests can seed data in an older schema before running the rest.
func migrateUpTo(db *sql.DB, last string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	var pending []string
	latest := ""
	for name := range applied {
		latest = max(latest, name)
	}
	for _, name := range names {
		if last != "" && name > last {
			break
		}
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	// A database without any applied migration is a fresh install with
	// nothing in it to lose.
	if len(pending) > 0 && len(applied) > 0 {
		if err := snapshotBeforeMigrate(db, latest); err != nil {
			return err
		}
	}

	for _, name := range pending {
		b, err := migrations.ReadFile("migrations/" + name)
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
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
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

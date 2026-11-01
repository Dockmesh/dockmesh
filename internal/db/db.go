package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

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
	// SQLITE_BUSY before the timeout applied. _txlock=immediate takes the
	// write lock at BEGIN, where busy_timeout waits for it: a deferred
	// transaction that reads first fails at once with SQLITE_BUSY when
	// another writer commits before its first write.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate", path)
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

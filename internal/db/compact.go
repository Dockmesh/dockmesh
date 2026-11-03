package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// compactPendingKey marks that plaintext was rewritten and the file still
// needs compacting. It stays set until a compaction finished, so a failed
// attempt is repeated on the next start.
const compactPendingKey = "db.compact_pending"

// Compact rewrites the database file and truncates the WAL. Rows written
// before secure_delete was on leave their old content in page slack;
// rebuilding the file drops it. A checkpoint blocked by a reader reports
// success in SQLite, so its result row is checked.
func Compact(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	var busy, logFrames, done int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("checkpoint blocked by another connection")
	}
	return nil
}

// MarkCompactPending records that the file must be compacted.
func MarkCompactPending(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, '1', CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, compactPendingKey)
	return err
}

// CompactIfPending compacts the file when a compaction is pending and
// clears the mark once it succeeded. Run it before background writers
// start: VACUUM holds the write lock for its whole run.
func CompactIfPending(ctx context.Context, db *sql.DB) error {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, compactPendingKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	start := time.Now()
	if err := Compact(ctx, db); err != nil {
		return fmt.Errorf("compact database: %w", err)
	}
	slog.Info("database compacted", "took", time.Since(start).Round(time.Millisecond))
	_, err = db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, compactPendingKey)
	return err
}

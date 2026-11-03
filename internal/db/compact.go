package db

import (
	"context"
	"database/sql"
)

// Compact rewrites the database file and truncates the WAL. Rows written
// before secure_delete was on leave their old content in page slack;
// rebuilding the file drops it.
func Compact(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

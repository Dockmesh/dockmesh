package secretsdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// sealedColumns are the columns the secrets-at-rest upgrade encrypts.
var sealedColumns = []struct{ table, column, where string }{
	{"backup_targets", "config_json", ""},
	{"backup_jobs", "target_config", ""},
	{"global_env", "value", " WHERE encrypted = 1 AND value <> ''"},
}

// sealedSince is the first migration of the release that encrypts stored
// secrets; a database without it still holds them in plaintext, as well
// as deploy history with filled-in .env values.
const sealedSince = "20261102125620"

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Verify checks that every secret in the sealed columns is encrypted and
// opens with sec.
func Verify(ctx context.Context, db *sql.DB, sec *secrets.Service) error {
	if sec == nil || !sec.Enabled() {
		return errors.New("secrets encryption is disabled")
	}
	var errs []error
	for _, c := range sealedColumns {
		rows, err := readColumn(ctx, db, c.table, c.column, c.where)
		if err != nil {
			return err
		}
		for id, raw := range rows {
			if err := verifyValue(c.table, raw, sec); err != nil {
				errs = append(errs, fmt.Errorf("%s row %d: %w", c.table, id, err))
			}
		}
	}
	return errors.Join(errs...)
}

func isPlain(table, raw string) bool {
	if table == "global_env" {
		return !secrets.IsSealed(raw)
	}
	return targets.HasPlainCredentials(raw)
}

func verifyValue(table, raw string, sec *secrets.Service) error {
	if isPlain(table, raw) {
		return errors.New("stored in plaintext")
	}
	if table == "global_env" {
		_, err := sec.OpenString(raw)
		return err
	}
	if !targets.HasCredentials(raw) {
		return nil
	}
	_, err := targets.OpenConfig(sec, raw)
	return err
}

// HoldsPlaintext reports whether db, typically a pre-migration snapshot,
// holds secrets in plaintext: it predates the encryption, or one of the
// sealed columns has a value that is not encrypted.
func HoldsPlaintext(ctx context.Context, db *sql.DB) (bool, error) {
	var sealed int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version LIKE ?`, sealedSince+"%").Scan(&sealed); err != nil {
		return false, err
	}
	if sealed == 0 {
		return true, nil
	}
	for _, c := range sealedColumns {
		rows, err := readColumn(ctx, db, c.table, c.column, c.where)
		if err != nil {
			return false, err
		}
		for _, raw := range rows {
			if isPlain(c.table, raw) {
				return true, nil
			}
		}
	}
	return false, nil
}

func readColumn(ctx context.Context, q queryer, table, column, where string) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, `+column+` FROM `+table+where) // #nosec G202 -- fixed table and column names
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var v sql.NullString
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v.String
	}
	return out, rows.Err()
}

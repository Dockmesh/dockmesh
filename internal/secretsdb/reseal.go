// Package secretsdb re-encrypts the secrets sealed into database columns
// when the secrets key is rotated.
package secretsdb

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// Reseal decrypts every sealed value in backup target configs, backup job
// target configs and secret global variables with oldSvc and seals it
// again with newSvc, in one transaction. It returns how many rows it
// rewrote. Rows oldSvc can't open are left as they are and logged, so one
// broken row doesn't block the rotation; the previous key stays in the
// key history and still opens everything else.
func Reseal(ctx context.Context, db *sql.DB, oldSvc, newSvc *secrets.Service) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	n := 0
	for _, c := range sealedColumns {
		rows, err := readColumn(ctx, tx, c.table, c.column, c.where)
		if err != nil {
			return 0, err
		}
		for id, raw := range rows {
			out, err := reseal(c.table, raw, oldSvc, newSvc)
			if err != nil {
				slog.Warn("key rotation: stored secret left under the previous key", "table", c.table, "id", id, "err", err)
				continue
			}
			if out == raw {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+c.table+` SET `+c.column+` = ? WHERE id = ?`, out, id); err != nil { // #nosec G202 -- fixed table and column names
				return 0, err
			}
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func reseal(table, raw string, oldSvc, newSvc *secrets.Service) (string, error) {
	if table == "global_env" {
		if !secrets.IsSealed(raw) {
			return raw, nil
		}
		plain, err := oldSvc.OpenString(raw)
		if err != nil {
			return "", err
		}
		return newSvc.SealString(plain)
	}
	if !targets.HasCredentials(raw) {
		return raw, nil
	}
	cfg, err := targets.OpenConfig(oldSvc, raw)
	if err != nil {
		return "", err
	}
	return targets.SealConfig(newSvc, cfg)
}

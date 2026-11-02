package backup

import (
	"context"
	"log/slog"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// EncryptStoredTargetSecrets encrypts target credentials still stored in
// plaintext, in saved targets and in job configs, and returns how many
// rows it rewrote. All rows change in one transaction; rows already
// encrypted are left alone, so running it again is a no-op.
func (s *Service) EncryptStoredTargetSecrets(ctx context.Context) (int, error) {
	if !s.canEncrypt() {
		return 0, nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	tables := []struct{ table, column string }{
		{"backup_targets", "config_json"},
		{"backup_jobs", "target_config"},
	}
	n := 0
	for _, t := range tables {
		rows, err := tx.QueryContext(ctx, `SELECT id, `+t.column+` FROM `+t.table) // #nosec G202 -- fixed table and column names
		if err != nil {
			return 0, err
		}
		type row struct {
			id  int64
			raw string
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.raw); err != nil {
				rows.Close()
				return 0, err
			}
			all = append(all, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		for _, r := range all {
			sealed, changed, err := targets.ResealIfPlain(s.secrets, r.raw)
			if err != nil {
				slog.Warn("backup: can't encrypt stored credentials", "table", t.table, "id", r.id, "err", err)
				continue
			}
			if !changed {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+t.table+` SET `+t.column+` = ? WHERE id = ?`, sealed, r.id); err != nil { // #nosec G202 -- fixed table and column names
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

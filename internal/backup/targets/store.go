package targets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// StoredTarget is one row of backup_targets.
type StoredTarget struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Config        any        `json:"config"`
	Status        string     `json:"status"`
	TotalBytes    int64      `json:"total_bytes"`
	UsedBytes     int64      `json:"used_bytes"`
	FreeBytes     int64      `json:"free_bytes"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	// JobCount is the number of backup jobs that write to this target.
	JobCount  int       `json:"job_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TargetInput struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config any    `json:"config"`
}

// TargetStore provides CRUD for the backup_targets table.
type TargetStore struct {
	db *sql.DB
}

func NewTargetStore(db *sql.DB) *TargetStore { return &TargetStore{db: db} }

func (s *TargetStore) List(ctx context.Context) ([]StoredTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, type, config_json, status, total_bytes, used_bytes,
		       last_checked_at, `+jobCountColumn+`, created_at, updated_at
		FROM backup_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Non-nil slice so the JSON response is [] not null.
	out := make([]StoredTarget, 0)
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *TargetStore) Get(ctx context.Context, id int64) (*StoredTarget, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, type, config_json, status, total_bytes, used_bytes,
		       last_checked_at, `+jobCountColumn+`, created_at, updated_at
		FROM backup_targets WHERE id = ?`, id)
	return scanTarget(row)
}

func (s *TargetStore) Create(ctx context.Context, in TargetInput) (*StoredTarget, error) {
	cfg, _ := json.Marshal(KeepHostKey("", nil, in.Type, in.Config))
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_targets (name, type, config_json) VALUES (?, ?, ?)`,
		in.Name, in.Type, string(cfg))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func (s *TargetStore) Update(ctx context.Context, id int64, in TargetInput) (*StoredTarget, error) {
	prev, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, _ := json.Marshal(KeepHostKey(prev.Type, prev.Config, in.Type, in.Config))
	_, err = s.db.ExecContext(ctx, `
		UPDATE backup_targets SET name = ?, type = ?, config_json = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, in.Name, in.Type, string(cfg), id)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// ErrTargetInUse refuses deleting a target that backup jobs write to;
// runs of deleted jobs count too, their archives are read from it.
var ErrTargetInUse = errors.New("backup target is used by backup jobs or their archives")

func (s *TargetStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM backup_targets WHERE id = ?
		  AND NOT EXISTS (SELECT 1 FROM backup_jobs WHERE target_id = ?)`, id, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var inUse bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM backup_jobs WHERE target_id = ?)`, id).Scan(&inUse); err == nil && inUse {
			return ErrTargetInUse
		}
	}
	return nil
}

func (s *TargetStore) UpdateStatus(ctx context.Context, id int64, status string, totalBytes, usedBytes int64) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_targets SET status = ?, total_bytes = ?, used_bytes = ?, last_checked_at = ?
		WHERE id = ?`, status, totalBytes, usedBytes, now, id)
	return err
}

// jobCountColumn counts the jobs (not deleted ones) that use a target.
const jobCountColumn = `(SELECT COUNT(*) FROM backup_jobs j
		WHERE j.target_id = backup_targets.id AND j.deleted_at IS NULL)`

type scanner interface{ Scan(dest ...any) error }

func scanTarget(r scanner) (*StoredTarget, error) {
	var t StoredTarget
	var cfgJSON string
	var checked sql.NullTime
	if err := r.Scan(&t.ID, &t.Name, &t.Type, &cfgJSON, &t.Status,
		&t.TotalBytes, &t.UsedBytes, &checked, &t.JobCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(cfgJSON), &t.Config)
	if checked.Valid {
		t.LastCheckedAt = &checked.Time
	}
	if t.TotalBytes > 0 {
		t.FreeBytes = t.TotalBytes - t.UsedBytes
	}
	return &t, nil
}

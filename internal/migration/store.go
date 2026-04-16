package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("migration not found")

// Store provides CRUD for the migrations table.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, m *Migration) error {
	progJSON := ""
	if m.Progress != nil {
		b, _ := json.Marshal(m.Progress)
		progJSON = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO migrations
			(id, stack_name, source_host_id, target_host_id, status, phase,
			 progress_json, started_at, initiated_by, drain_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.StackName, m.SourceHostID, m.TargetHostID,
		m.Status, m.Phase, progJSON, m.StartedAt,
		m.InitiatedBy, m.DrainID)
	return err
}

func (s *Store) UpdateStatus(ctx context.Context, id, status, phase, errMsg string) error {
	var completedAt *time.Time
	if status == StatusCompleted || status == StatusFailed || status == StatusRolledBack {
		now := time.Now()
		completedAt = &now
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE migrations SET status = ?, phase = ?, error_message = ?, completed_at = ?
		WHERE id = ?`, status, phase, errMsg, completedAt, id)
	return err
}

func (s *Store) UpdateProgress(ctx context.Context, id string, p *Progress) error {
	b, _ := json.Marshal(p)
	_, err := s.db.ExecContext(ctx, `UPDATE migrations SET progress_json = ? WHERE id = ?`, string(b), id)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (*Migration, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, stack_name, source_host_id, target_host_id, status, phase,
		       progress_json, started_at, completed_at, error_message,
		       initiated_by, drain_id, created_at
		FROM migrations WHERE id = ?`, id)
	return scanMigration(row)
}

func (s *Store) ListByStack(ctx context.Context, stackName string) ([]*Migration, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, stack_name, source_host_id, target_host_id, status, phase,
		       progress_json, started_at, completed_at, error_message,
		       initiated_by, drain_id, created_at
		FROM migrations WHERE stack_name = ? ORDER BY created_at DESC`, stackName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMigrations(rows)
}

func (s *Store) ListAll(ctx context.Context, limit int) ([]*Migration, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, stack_name, source_host_id, target_host_id, status, phase,
		       progress_json, started_at, completed_at, error_message,
		       initiated_by, drain_id, created_at
		FROM migrations ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMigrations(rows)
}

func (s *Store) ListActive(ctx context.Context) ([]*Migration, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, stack_name, source_host_id, target_host_id, status, phase,
		       progress_json, started_at, completed_at, error_message,
		       initiated_by, drain_id, created_at
		FROM migrations
		WHERE status NOT IN ('completed', 'failed', 'rolled_back')
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMigrations(rows)
}

// HasActive returns true if the given stack has any non-terminal migration.
func (s *Store) HasActive(ctx context.Context, stackName string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM migrations
		WHERE stack_name = ? AND status NOT IN ('completed', 'failed', 'rolled_back')`,
		stackName).Scan(&count)
	return count > 0, err
}

type scanner interface{ Scan(dest ...any) error }

func scanMigration(r scanner) (*Migration, error) {
	var m Migration
	var progJSON, phase, errMsg, drainID sql.NullString
	var started, completed sql.NullTime
	if err := r.Scan(
		&m.ID, &m.StackName, &m.SourceHostID, &m.TargetHostID,
		&m.Status, &phase, &progJSON, &started, &completed,
		&errMsg, &m.InitiatedBy, &drainID, &m.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.Phase = phase.String
	m.ErrorMessage = errMsg.String
	m.DrainID = drainID.String
	if started.Valid {
		m.StartedAt = &started.Time
	}
	if completed.Valid {
		m.CompletedAt = &completed.Time
	}
	if progJSON.Valid && progJSON.String != "" {
		var p Progress
		if json.Unmarshal([]byte(progJSON.String), &p) == nil {
			m.Progress = &p
		}
	}
	return &m, nil
}

func scanMigrations(rows *sql.Rows) ([]*Migration, error) {
	var out []*Migration
	for rows.Next() {
		m, err := scanMigration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

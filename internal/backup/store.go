package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type store struct {
	db *sql.DB
}

func newStore(db *sql.DB) *store { return &store{db: db} }

// -----------------------------------------------------------------------------
// jobs
// -----------------------------------------------------------------------------

func (s *store) listJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, target_type, target_config, sources, schedule,
		       retention_count, retention_days, encrypt, pre_hooks, post_hooks,
		       enabled, last_run_at, next_run_at, created_at, updated_at
		FROM backup_jobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *store) getJob(ctx context.Context, id int64) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, target_type, target_config, sources, schedule,
		       retention_count, retention_days, encrypt, pre_hooks, post_hooks,
		       enabled, last_run_at, next_run_at, created_at, updated_at
		FROM backup_jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	return j, err
}

func (s *store) createJob(ctx context.Context, in JobInput) (int64, error) {
	tcfg, _ := json.Marshal(in.TargetConfig)
	srcs, _ := json.Marshal(in.Sources)
	pre, _ := json.Marshal(in.PreHooks)
	post, _ := json.Marshal(in.PostHooks)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_jobs
			(name, target_type, target_config, sources, schedule,
			 retention_count, retention_days, encrypt, pre_hooks, post_hooks, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.TargetType, string(tcfg), string(srcs), in.Schedule,
		in.RetentionCount, in.RetentionDays, boolInt(in.Encrypt),
		string(pre), string(post), boolInt(in.Enabled))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *store) updateJob(ctx context.Context, id int64, in JobInput) error {
	tcfg, _ := json.Marshal(in.TargetConfig)
	srcs, _ := json.Marshal(in.Sources)
	pre, _ := json.Marshal(in.PreHooks)
	post, _ := json.Marshal(in.PostHooks)
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_jobs SET
			name = ?, target_type = ?, target_config = ?, sources = ?,
			schedule = ?, retention_count = ?, retention_days = ?,
			encrypt = ?, pre_hooks = ?, post_hooks = ?, enabled = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Name, in.TargetType, string(tcfg), string(srcs), in.Schedule,
		in.RetentionCount, in.RetentionDays, boolInt(in.Encrypt),
		string(pre), string(post), boolInt(in.Enabled), id)
	return err
}

func (s *store) deleteJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id = ?`, id)
	return err
}

func (s *store) updateJobRunTimes(ctx context.Context, id int64, last, next *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET last_run_at = ?, next_run_at = ? WHERE id = ?`,
		last, next, id)
	return err
}

// -----------------------------------------------------------------------------
// runs
// -----------------------------------------------------------------------------

func (s *store) startRun(ctx context.Context, j *Job) (int64, error) {
	srcs, _ := json.Marshal(j.Sources)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_runs (job_id, job_name, status, sources_json, encrypted)
		VALUES (?, ?, 'running', ?, ?)`,
		j.ID, j.Name, string(srcs), boolInt(j.Encrypt))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *store) finishRun(ctx context.Context, runID int64, status string, size int64, path, sha string, runErr error) error {
	now := time.Now()
	var errStr string
	if runErr != nil {
		errStr = runErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = ?, finished_at = ?, size_bytes = ?,
			target_path = ?, sha256 = ?, error = ?
		WHERE id = ?`,
		status, now, size, path, sha, errStr, runID)
	return err
}

func (s *store) listRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, job_name, status, started_at, finished_at,
		       size_bytes, COALESCE(target_path, ''), COALESCE(sha256, ''),
		       encrypted, COALESCE(error, ''), sources_json
		FROM backup_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var finished sql.NullTime
		var encInt int
		var srcs string
		if err := rows.Scan(&r.ID, &r.JobID, &r.JobName, &r.Status, &r.StartedAt,
			&finished, &r.SizeBytes, &r.TargetPath, &r.SHA256, &encInt, &r.Error, &srcs); err != nil {
			return nil, err
		}
		if finished.Valid {
			t := finished.Time
			r.FinishedAt = &t
		}
		r.Encrypted = encInt == 1
		_ = json.Unmarshal([]byte(srcs), &r.Sources)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) getRun(ctx context.Context, id int64) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, job_id, job_name, status, started_at, finished_at,
		       size_bytes, COALESCE(target_path, ''), COALESCE(sha256, ''),
		       encrypted, COALESCE(error, ''), sources_json
		FROM backup_runs WHERE id = ?`, id)
	var r Run
	var finished sql.NullTime
	var encInt int
	var srcs string
	if err := row.Scan(&r.ID, &r.JobID, &r.JobName, &r.Status, &r.StartedAt,
		&finished, &r.SizeBytes, &r.TargetPath, &r.SHA256, &encInt, &r.Error, &srcs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if finished.Valid {
		t := finished.Time
		r.FinishedAt = &t
	}
	r.Encrypted = encInt == 1
	_ = json.Unmarshal([]byte(srcs), &r.Sources)
	return &r, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(r rowScanner) (*Job, error) {
	var j Job
	var tcfg, srcs, pre, post string
	var enabled, encrypt int
	var lastRun, nextRun sql.NullTime
	if err := r.Scan(
		&j.ID, &j.Name, &j.TargetType, &tcfg, &srcs, &j.Schedule,
		&j.RetentionCount, &j.RetentionDays, &encrypt, &pre, &post,
		&enabled, &lastRun, &nextRun, &j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	j.Enabled = enabled == 1
	j.Encrypt = encrypt == 1
	_ = json.Unmarshal([]byte(tcfg), &j.TargetConfig)
	_ = json.Unmarshal([]byte(srcs), &j.Sources)
	_ = json.Unmarshal([]byte(pre), &j.PreHooks)
	_ = json.Unmarshal([]byte(post), &j.PostHooks)
	if lastRun.Valid {
		t := lastRun.Time
		j.LastRunAt = &t
	}
	if nextRun.Valid {
		t := nextRun.Time
		j.NextRunAt = &t
	}
	return &j, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

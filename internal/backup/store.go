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
		SELECT id, name, COALESCE(host_id, ''), COALESCE(target_id, 0), target_type, target_config, sources, schedule,
		       retention_count, retention_days, encrypt, pre_hooks, post_hooks,
		       enabled, COALESCE(needs_review, 0), COALESCE(review_reason, ''),
		       COALESCE(review_acked, 0),
		       last_run_at, next_run_at, created_at, updated_at
		FROM backup_jobs WHERE deleted_at IS NULL ORDER BY id`)
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
	return s.queryJob(ctx, id, false)
}

// getJobForRun also returns deleted jobs: their runs stay restorable and
// need the job's target.
// jobsUsingTarget returns every job that writes to the target, deleted
// ones included: their runs still read from it.
func (s *store) jobsUsingTarget(ctx context.Context, targetID int64) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM backup_jobs WHERE target_id = ? ORDER BY id`, targetID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(ids))
	for _, id := range ids {
		j, err := s.getJobForRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, nil
}

func (s *store) getJobForRun(ctx context.Context, id int64) (*Job, error) {
	return s.queryJob(ctx, id, true)
}

func (s *store) queryJob(ctx context.Context, id int64, includeDeleted bool) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(host_id, ''), COALESCE(target_id, 0), target_type, target_config, sources, schedule,
		       retention_count, retention_days, encrypt, pre_hooks, post_hooks,
		       enabled, COALESCE(needs_review, 0), COALESCE(review_reason, ''),
		       COALESCE(review_acked, 0),
		       last_run_at, next_run_at, created_at, updated_at
		FROM backup_jobs WHERE id = ? AND (? OR deleted_at IS NULL)`, id, includeDeleted)
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
			(name, host_id, target_id, target_type, target_config, sources, schedule,
			 retention_count, retention_days, encrypt, pre_hooks, post_hooks, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.HostID, nullID(in.TargetID), in.TargetType, string(tcfg), string(srcs), in.Schedule,
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
			name = ?, host_id = ?, target_id = ?, target_type = ?, target_config = ?, sources = ?,
			schedule = ?, retention_count = ?, retention_days = ?,
			encrypt = ?, pre_hooks = ?, post_hooks = ?, enabled = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Name, in.HostID, nullID(in.TargetID), in.TargetType, string(tcfg), string(srcs), in.Schedule,
		in.RetentionCount, in.RetentionDays, boolInt(in.Encrypt),
		string(pre), string(post), boolInt(in.Enabled), id)
	return err
}

// deleteJob hides the job and frees its name; a job with no archive left
// is removed for good.
func (s *store) deleteJob(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM backup_jobs WHERE id = ? AND deleted_at IS NULL AND NOT EXISTS (
			SELECT 1 FROM backup_runs WHERE job_id = ? AND pruned_at IS NULL
			  AND COALESCE(target_path, '') != '')`, id, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	res, err = s.db.ExecContext(ctx, `
		UPDATE backup_jobs SET deleted_at = CURRENT_TIMESTAMP, enabled = 0,
			name = name || ' (deleted #' || id || ')'
		WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrJobNotFound
	}
	return nil
}

func (s *store) updateJobRunTimes(ctx context.Context, id int64, last, next *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET last_run_at = ?, next_run_at = ? WHERE id = ?`,
		last, next, id)
	return err
}

// -----------------------------------------------------------------------------
// run logs
// -----------------------------------------------------------------------------

// RunLogEntry is one captured line of a backup run.
type RunLogEntry struct {
	ID     int64     `json:"id"`
	TS     time.Time `json:"ts"`
	Stream string    `json:"stream"`
	Line   string    `json:"line"`
}

// appendRunLog inserts a single log row. Best-effort: errors are
// returned so callers can choose to log them, but the executor's caller
// must not treat a log-write failure as a backup failure.
func (s *store) appendRunLog(ctx context.Context, runID int64, stream, line string) error {
	if line == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_run_logs (run_id, stream, line) VALUES (?, ?, ?)`,
		runID, stream, line)
	return err
}

// listRunLogs returns all captured lines for a run in chronological
// order (by insertion id, which matches ts ordering since the executor
// writes serially).
func (s *store) listRunLogs(ctx context.Context, runID int64) ([]RunLogEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, stream, line FROM backup_run_logs
		 WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunLogEntry
	for rows.Next() {
		var e RunLogEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Stream, &e.Line); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// runs
// -----------------------------------------------------------------------------

func (s *store) startRun(ctx context.Context, j *Job) (int64, error) {
	srcs, _ := json.Marshal(j.Sources)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_runs (job_id, job_name, status, sources_json, encrypted)
		VALUES (?, ?, ?, ?, ?)`,
		j.ID, j.Name, StatusRunning, string(srcs), boolInt(j.Encrypt))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// failInterruptedRuns fails runs left in state running by a process that
// stopped mid-run. Their target_path stays without a hash, so the next
// retention pass of the job removes the partial archive.
func (s *store) failInterruptedRuns(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET status = ?, finished_at = ?, sha256 = '',
			error = 'interrupted: the server stopped during this run'
		WHERE status = ?`, StatusFailed, time.Now(), StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *store) setRunPath(ctx context.Context, runID int64, path string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET target_path = ? WHERE id = ?`, path, runID)
	return err
}

// finishRun records the outcome; sha covers the archive as stored on the target.
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

// keptRuns returns the job's runs whose archive is still on the target,
// newest first. Retention only deletes files listed here, so it can't
// touch another job's archives or files Dockmesh didn't record.
func (s *store) keptRuns(ctx context.Context, jobID int64) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, target_path, finished_at
		FROM backup_runs
		WHERE job_id = ? AND status IN ('success', 'success_with_warnings')
		  AND pruned_at IS NULL AND COALESCE(target_path, '') != ''
		ORDER BY id DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var finished sql.NullTime
		if err := rows.Scan(&r.ID, &r.TargetPath, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			r.FinishedAt = &finished.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// partialRuns lists failed runs that still point at a file: partials that
// could not be deleted when the run failed (no hash means the archive
// never completed).
func (s *store) partialRuns(ctx context.Context, jobID int64) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, target_path FROM backup_runs
		WHERE job_id = ? AND status = 'failed' AND pruned_at IS NULL
		  AND COALESCE(target_path, '') != '' AND COALESCE(sha256, '') = ''`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.TargetPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) markPruned(ctx context.Context, runID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_runs SET pruned_at = CURRENT_TIMESTAMP WHERE id = ?`, runID)
	return err
}

func (s *store) listRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	return s.queryRuns(ctx, `
		SELECT id, job_id, job_name, status, started_at, finished_at,
		       size_bytes, COALESCE(target_path, ''), COALESCE(sha256, ''),
		       encrypted, COALESCE(error, ''), sources_json, pruned_at
		FROM backup_runs ORDER BY id DESC LIMIT ?`, limit)
}

// latestRuns returns the most recent run of every job that has one.
func (s *store) latestRuns(ctx context.Context) ([]Run, error) {
	return s.queryRuns(ctx, `
		SELECT id, job_id, job_name, status, started_at, finished_at,
		       size_bytes, COALESCE(target_path, ''), COALESCE(sha256, ''),
		       encrypted, COALESCE(error, ''), sources_json, pruned_at
		FROM backup_runs
		WHERE id IN (SELECT MAX(id) FROM backup_runs GROUP BY job_id)
		ORDER BY id DESC`)
}

func (s *store) queryRuns(ctx context.Context, query string, args ...any) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var finished, pruned sql.NullTime
		var encInt int
		var srcs string
		if err := rows.Scan(&r.ID, &r.JobID, &r.JobName, &r.Status, &r.StartedAt,
			&finished, &r.SizeBytes, &r.TargetPath, &r.SHA256, &encInt, &r.Error, &srcs, &pruned); err != nil {
			return nil, err
		}
		if finished.Valid {
			t := finished.Time
			r.FinishedAt = &t
		}
		if pruned.Valid {
			t := pruned.Time
			r.PrunedAt = &t
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
		       encrypted, COALESCE(error, ''), sources_json, pruned_at
		FROM backup_runs WHERE id = ?`, id)
	var r Run
	var finished, pruned sql.NullTime
	var encInt int
	var srcs string
	if err := row.Scan(&r.ID, &r.JobID, &r.JobName, &r.Status, &r.StartedAt,
		&finished, &r.SizeBytes, &r.TargetPath, &r.SHA256, &encInt, &r.Error, &srcs, &pruned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if finished.Valid {
		t := finished.Time
		r.FinishedAt = &t
	}
	if pruned.Valid {
		t := pruned.Time
		r.PrunedAt = &t
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
	var enabled, encrypt, needsReview, reviewAcked int
	var lastRun, nextRun sql.NullTime
	if err := r.Scan(
		&j.ID, &j.Name, &j.HostID, &j.TargetID, &j.TargetType, &tcfg, &srcs, &j.Schedule,
		&j.RetentionCount, &j.RetentionDays, &encrypt, &pre, &post,
		&enabled, &needsReview, &j.ReviewReason, &reviewAcked,
		&lastRun, &nextRun, &j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	j.Enabled = enabled == 1
	j.Encrypt = encrypt == 1
	j.NeedsReview = needsReview == 1
	j.ReviewAcked = reviewAcked == 1
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

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *store) setJobEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		boolInt(enabled), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrJobNotFound
	}
	return nil
}

// markJobNeedsReview flags a job for operator review with a reason.
// Used by MarkAutoCreatedJobsForReview when a legacy default job is
// found on an existing install. P.13.2.
func (s *store) markJobNeedsReview(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET needs_review = 1, review_reason = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		reason, id)
	return err
}

// clearJobReview drops the review flag and sets review_acked so the
// boot-time migration leaves this job alone forever. Called when the
// operator picks Keep or Disable in the UI.
func (s *store) clearJobReview(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET needs_review = 0, review_reason = '', review_acked = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		id)
	return err
}

func (s *store) setJobTargetConfig(ctx context.Context, id int64, cfg any) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET target_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(b), id)
	return err
}

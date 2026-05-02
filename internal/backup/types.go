// Package backup implements scheduled volume + stack backups with
// optional age encryption, local + S3 targets, and pre/post hooks for
// application-consistent snapshots (concept §3.4).
package backup

import (
	"errors"
	"time"
)

// Job is one row of backup_jobs.
type Job struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	HostID         string     `json:"host_id,omitempty"`
	TargetType     string     `json:"target_type"`
	TargetConfig   any        `json:"target_config"`
	Sources        []Source   `json:"sources"`
	Schedule       string     `json:"schedule"`
	RetentionCount int        `json:"retention_count"`
	RetentionDays  int        `json:"retention_days"`
	Encrypt        bool       `json:"encrypt"`
	PreHooks       []Hook     `json:"pre_hooks"`
	PostHooks      []Hook     `json:"post_hooks"`
	Enabled        bool       `json:"enabled"`
	// NeedsReview is true when the job was auto-created by an older
	// version of dockmesh and the operator has not yet confirmed they
	// want to keep it. New installs never set this. P.13.2.
	NeedsReview  bool   `json:"needs_review,omitempty"`
	ReviewReason string `json:"review_reason,omitempty"`
	// ReviewAcked stays true forever once the operator clears the
	// review (Keep / Disable). Used by the boot-time migration to
	// skip jobs that were already reviewed in a previous boot.
	ReviewAcked bool `json:"-"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// JobInput is the create/update payload from the API.
type JobInput struct {
	Name           string   `json:"name"`
	HostID         string   `json:"host_id,omitempty"` // empty or "local" → central daemon. P.12 multi-host backup.
	TargetType     string   `json:"target_type"`
	TargetConfig   any      `json:"target_config"`
	Sources        []Source `json:"sources"`
	Schedule       string   `json:"schedule"`
	RetentionCount int      `json:"retention_count"`
	RetentionDays  int      `json:"retention_days"`
	Encrypt        bool     `json:"encrypt"`
	PreHooks       []Hook   `json:"pre_hooks"`
	PostHooks      []Hook   `json:"post_hooks"`
	Enabled        bool     `json:"enabled"`
}

// Source describes one thing to back up.
//   - type=volume snapshots a single docker volume.
//   - type=stack  snapshots a stack's compose+env+meta files plus all
//     named volumes referenced by the stack.
//   - type=system snapshots the Dockmesh server itself: SQLite DB (via
//     VACUUM INTO for a consistent point-in-time copy), the /stacks
//     filesystem, and the data directory (secrets keys, jwt secret,
//     audit genesis). Used by the default daily backup job.
type Source struct {
	Type string `json:"type"` // "volume" | "stack" | "system"
	Name string `json:"name"`
}

// SystemPaths tells the Executor where the server's own files live so
// the "system" source can snapshot them. Injected at NewService time —
// the backup package cannot import internal/config, and hard-coding the
// defaults would break non-default layouts.
type SystemPaths struct {
	DBPath     string // sqlite DB file (absolute or CWD-relative)
	StacksRoot string // /stacks root containing per-stack compose dirs
	DataDir    string // data dir with secrets.env, secrets.age-key, audit-genesis.sha256
}

// Hook is a docker exec invocation against a running container, used to
// quiesce databases (e.g. pg_dump) before the tar snapshot.
type Hook struct {
	Container string   `json:"container"`
	Cmd       []string `json:"cmd"`
}

// Run is one row of backup_runs.
type Run struct {
	ID         int64      `json:"id"`
	JobID      int64      `json:"job_id"`
	JobName    string     `json:"job_name"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	SizeBytes  int64      `json:"size_bytes"`
	TargetPath string     `json:"target_path,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	Encrypted  bool       `json:"encrypted"`
	Error      string     `json:"error,omitempty"`
	Sources    []Source   `json:"sources"`
}

// Common errors.
var (
	ErrUnknownTargetType = errors.New("unknown target type")
	ErrUnknownSourceType = errors.New("unknown source type")
	ErrJobNotFound       = errors.New("job not found")
	ErrRunNotFound       = errors.New("run not found")
)

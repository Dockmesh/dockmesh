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
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	HostID       string `json:"host_id,omitempty"`
	TargetID     int64  `json:"target_id,omitempty"`
	TargetType   string `json:"target_type"`
	TargetConfig any    `json:"target_config"`
	// CredentialsError is set when the inline target's stored credentials
	// can't be decrypted.
	CredentialsError string `json:"credentials_error,omitempty"`
	unreadable       []string
	Sources          []Source `json:"sources"`
	Schedule         string   `json:"schedule"`
	RetentionCount   int      `json:"retention_count"`
	RetentionDays    int      `json:"retention_days"`
	Encrypt          bool     `json:"encrypt"`
	PreHooks         []Hook   `json:"pre_hooks"`
	PostHooks        []Hook   `json:"post_hooks"`
	Enabled          bool     `json:"enabled"`
	// NeedsReview is true when the job was auto-created by an older
	// version of dockmesh and the operator has not yet confirmed they
	// want to keep it. New installs never set this.
	NeedsReview  bool   `json:"needs_review,omitempty"`
	ReviewReason string `json:"review_reason,omitempty"`
	// ReviewAcked stays true forever once the operator clears the
	// review (Keep / Disable). Used by the boot-time migration to
	// skip jobs that were already reviewed in a previous boot.
	ReviewAcked bool       `json:"-"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// RunsAtPreviousLocation is set in the answer to a save that moved the
	// job to another target: the kept archives that stay where they were
	// written and remain restorable from there.
	RunsAtPreviousLocation int `json:"runs_at_previous_location,omitempty"`
}

// JobInput is the create/update payload from the API.
type JobInput struct {
	Name   string `json:"name"`
	HostID string `json:"host_id,omitempty"` // empty or "local" → central daemon
	// TargetID picks a saved target. Without it, TargetType/TargetConfig
	// describe a new target that is saved under the job's name.
	TargetID       int64    `json:"target_id,omitempty"`
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
	ID      int64  `json:"id"`
	JobID   int64  `json:"job_id"`
	JobName string `json:"job_name"`
	// HostID is the host the run was taken on ("local" for the server).
	HostID     string     `json:"host_id"`
	Status     string     `json:"status"` // running | success | success_with_warnings | failed
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	SizeBytes  int64      `json:"size_bytes"`
	TargetPath string     `json:"target_path,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	Encrypted  bool       `json:"encrypted"`
	Error      string     `json:"error,omitempty"`
	Sources    []Source   `json:"sources"`
	// PrunedAt is set once the archive was deleted from the target.
	PrunedAt *time.Time `json:"pruned_at,omitempty"`
	// ArchiveMissingAt is set when the archive was looked for on a
	// reachable target and wasn't there; ArchiveMissingReason says where
	// it was looked for. Both clear once the archive is read again.
	ArchiveMissingAt     *time.Time `json:"archive_missing_at,omitempty"`
	ArchiveMissingReason string     `json:"archive_missing_reason,omitempty"`
	// TargetID is the saved target the run wrote to; runs from before
	// this was recorded have none.
	TargetID int64 `json:"target_id,omitempty"`
	// Location is where the archive went, nil for runs from before it was
	// recorded. Kept off the API: it names servers and paths of targets a
	// caller may not see.
	Location *RunLocation `json:"-"`
}

// Run statuses. A post-hook failure leaves an intact archive, so it is
// a success with warnings rather than a failure.
const (
	StatusRunning             = "running"
	StatusSuccess             = "success"
	StatusSuccessWithWarnings = "success_with_warnings"
	StatusFailed              = "failed"
)

// Restorable reports whether a run's archive can be read back.
func (r *Run) Restorable() bool {
	return (r.Status == StatusSuccess || r.Status == StatusSuccessWithWarnings) && r.PrunedAt == nil
}

// ErrArchivePresent refuses to remove a run from the list whose archive
// was not found missing.
var ErrArchivePresent = errors.New("only a run whose archive is missing can be removed from the list; a run with an archive goes through the job's retention")

// ErrNotRestorable is returned when a run failed or its archive is gone.
var ErrNotRestorable = errors.New("run has no restorable archive (failed, still running, or removed by retention)")

// Common errors.
var (
	ErrUnknownTargetType = errors.New("unknown target type")
	ErrUnknownSourceType = errors.New("unknown source type")
	ErrJobNotFound       = errors.New("job not found")
	// ErrEncryptUnavailable refuses encrypted backups while the secrets
	// service is disabled: the archive would be written in plaintext.
	ErrEncryptUnavailable = errors.New("encryption needs secrets encryption enabled on the server (DOCKMESH_SECRETS_ENCRYPT_ENV)")
	ErrRunNotFound        = errors.New("run not found")
	// ErrSystemRestore refuses to unpack a system backup anywhere a
	// container could read it; system backups replace the whole install.
	ErrSystemRestore  = errors.New("system backups can't be restored into a volume or stack; restore them as a whole (Restore the whole system, the setup wizard or `dockmesh restore`)")
	ErrTargetNotFound = errors.New("backup target not found")
)

// HasSystemSource reports whether sources include the server itself.
func HasSystemSource(sources []Source) bool {
	for _, src := range sources {
		if src.Type == "system" {
			return true
		}
	}
	return false
}

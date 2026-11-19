// Package migration implements stack migration between hosts.
// Safe Mode: stop source → start target → verify → cleanup.
package migration

import (
	"errors"
	"fmt"
	"time"
)

// Status constants for the migration state machine.
const (
	StatusPending     = "pending"
	StatusPreflight   = "preflight"
	StatusPreparing   = "preparing"
	StatusPreDump     = "pre_dump"
	StatusStopping    = "stopping"
	StatusSyncing     = "syncing"
	StatusStarting    = "starting"
	StatusPostRestore = "post_restore"
	StatusHealthCheck = "health_check"
	StatusCommitting  = "committing"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusRolledBack  = "rolled_back"
	// StatusRollingBack is a migration whose stack is being restored on
	// the source; its phase stays the one it failed or stopped in.
	StatusRollingBack = "rolling_back"
)

// Migration is one row of the migrations table.
type Migration struct {
	ID           string     `json:"id"`
	StackName    string     `json:"stack_name"`
	SourceHostID string     `json:"source_host_id"`
	TargetHostID string     `json:"target_host_id"`
	Status       string     `json:"status"`
	Phase        string     `json:"phase,omitempty"`
	Progress     *Progress  `json:"progress,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	InitiatedBy  string     `json:"initiated_by"`
	DrainID      string     `json:"drain_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Progress is the in-flight progress record, stored as JSON in the DB
// and updated every 2s during transfer phases.
type Progress struct {
	// Volume being transferred (empty outside syncing phase).
	CurrentVolume string `json:"current_volume,omitempty"`
	VolumeIndex   int    `json:"volume_index"`
	VolumesTotal  int    `json:"volumes_total"`
	BytesTotal    int64  `json:"bytes_total"`
	BytesDone     int64  `json:"bytes_done"`
	// Images being pulled on target (preparing phase).
	ImagesPulled int `json:"images_pulled"`
	ImagesTotal  int `json:"images_total"`
	// CreatedVolumes are the volumes the migration created on the target;
	// a rollback before the commit removes them again.
	CreatedVolumes []string `json:"created_volumes,omitempty"`
	// VolumesCopied are the volumes copied and checked so far.
	VolumesCopied []CopiedVolume `json:"volumes_copied,omitempty"`
	// FailedPhase is the phase a failed migration stopped in.
	FailedPhase string `json:"failed_phase,omitempty"`
	// Undo lists what the rollback did, in order: UndoStopTarget,
	// UndoRemoveVolumes, UndoKeepVolumes, UndoRestartSource.
	Undo []string `json:"undo,omitempty"`
}

// CopiedVolume is one volume a migration copied to the target.
type CopiedVolume struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	Entries int    `json:"entries"`
}

// Rollback steps recorded in Progress.Undo.
const (
	UndoStopTarget    = "stop_target"
	UndoRemoveVolumes = "remove_target_volumes"
	// UndoKeepVolumes: a completed migration's volumes on the target hold
	// what the stack wrote there, so its rollback leaves them.
	UndoKeepVolumes   = "keep_target_volumes"
	UndoRestartSource = "restart_source"
)

// progress returns m's progress record, creating it.
func (m *Migration) progress() *Progress {
	if m.Progress == nil {
		m.Progress = &Progress{}
	}
	return m.Progress
}

// MigrateRequest is the API payload to initiate a migration.
type MigrateRequest struct {
	TargetHostID string `json:"target_host_id"`
	// ConfirmHooks is the digest of the hook commands the caller was
	// shown; needed when the stack has any.
	ConfirmHooks string `json:"confirm_hooks,omitempty"`
}

// PreflightCheck is one item in the pre-flight report.
type PreflightCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	// Status is CheckPass, CheckFail or CheckUnknown. An unknown check
	// could not be evaluated; it is reported but doesn't block.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

const (
	CheckPass    = "pass"
	CheckFail    = "fail"
	CheckUnknown = "unknown"
)

// PreflightResult groups all pre-flight checks.
type PreflightResult struct {
	Passed bool             `json:"passed"`
	Checks []PreflightCheck `json:"checks"`
	// Hooks are the commands the migration would run, nil without any.
	Hooks *HookCommands `json:"hooks,omitempty"`
}

// IsTerminal returns true if the migration is in a final state.
func (m *Migration) IsTerminal() bool {
	return m.Status == StatusCompleted || m.Status == StatusFailed || m.Status == StatusRolledBack
}

var ErrNotCompleted = fmt.Errorf("migration not in completed state")

// ErrNamedVolumes keeps stacks with data out of a drain: each one is
// down while its volumes copy, so they are moved one at a time.
var ErrNamedVolumes = errors.New("stack uses named volumes; move it on its own with Migrate, which copies their data")

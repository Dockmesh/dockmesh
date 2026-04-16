// Package migration implements stack migration between hosts (P.9).
// Safe Mode: stop source → transfer volumes → start target → verify →
// cleanup. No warm sync in Phase 1.
package migration

import (
	"time"
)

// Status constants for the migration state machine.
const (
	StatusPending      = "pending"
	StatusPreflight    = "preflight"
	StatusPreparing    = "preparing"
	StatusPreDump      = "pre_dump"
	StatusStopping     = "stopping"
	StatusSyncing      = "syncing"
	StatusStarting     = "starting"
	StatusPostRestore  = "post_restore"
	StatusHealthCheck  = "health_check"
	StatusCommitting   = "committing"
	StatusCompleted    = "completed"
	StatusFailed       = "failed"
	StatusRolledBack   = "rolled_back"
)

// Migration is one row of the migrations table.
type Migration struct {
	ID            string     `json:"id"`
	StackName     string     `json:"stack_name"`
	SourceHostID  string     `json:"source_host_id"`
	TargetHostID  string     `json:"target_host_id"`
	Status        string     `json:"status"`
	Phase         string     `json:"phase,omitempty"`
	Progress      *Progress  `json:"progress,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	InitiatedBy   string     `json:"initiated_by"`
	DrainID       string     `json:"drain_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
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
	ImagesPulled  int    `json:"images_pulled"`
	ImagesTotal   int    `json:"images_total"`
}

// MigrateRequest is the API payload to initiate a migration.
type MigrateRequest struct {
	TargetHostID string `json:"target_host_id"`
}

// PreflightCheck is one item in the pre-flight report.
type PreflightCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// PreflightResult groups all pre-flight checks.
type PreflightResult struct {
	Passed bool             `json:"passed"`
	Checks []PreflightCheck `json:"checks"`
}

// IsTerminal returns true if the migration is in a final state.
func (m *Migration) IsTerminal() bool {
	return m.Status == StatusCompleted || m.Status == StatusFailed || m.Status == StatusRolledBack
}

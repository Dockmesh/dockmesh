package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/system"
)

// systemMetricsRow is the all-mode row for the system metrics fan-out.
// Embeds system.Metrics so its fields (cpu_percent, mem_percent, …)
// flatten into the response object alongside host_id / host_name, which
// keeps the frontend's per-host mini-table readable without an extra
// .metrics indirection.
type systemMetricsRow struct {
	system.Metrics
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

// SystemMetrics returns a host-level metrics snapshot (CPU / memory /
// disk / uptime) for the dashboard's System Health panel.
//
// Routing:
//   - ?host=local (default): returns a bare system.Metrics struct for
//     the central Dockmesh server.
//   - ?host=<id>: returns the metrics for a specific agent, fetched
//     over the agent protocol. Requires the agent to be online.
//   - ?host=all: fans out to local + every online agent and returns
//     a FanOutResponse with one row per host. Each row carries host_id
//     and host_name so the frontend can render a per-host mini-table.
func (h *Handlers) SystemMetrics(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	// All-mode fan-out: collect one Metrics sample per online host.
	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.Hosts.PickAll(r.Context())
		res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]systemMetricsRow, error) {
			m, err := hh.SystemMetrics(ctx)
			if err != nil {
				return nil, err
			}
			return []systemMetricsRow{{
				Metrics:  m,
				HostID:   hh.ID(),
				HostName: hh.Name(),
			}}, nil
		})
		writeJSON(w, http.StatusOK, res)
		return
	}

	// Single-host path. For local we call the system package directly
	// to avoid the host.Host interface overhead; for remote we go
	// through the agent protocol via pickHost.
	if hostID == "" || hostID == "local" {
		writeJSON(w, http.StatusOK, system.Collect())
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	m, err := target.SystemMetrics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// BackupStatusResponse powers the sidebar "last backup" pill. States:
//   - "never"   — default job doesn't exist or has never run
//   - "ok"      — most recent run succeeded ≤ 36 h ago
//   - "stale"   — most recent run succeeded but is older than 36 h
//   - "failed"  — most recent run's status is not "success"
//   - "disabled"— default job exists but is disabled
//
// 36 h is the staleness threshold: daily cadence + 12 h grace so a job
// that runs slightly late on a heavily loaded server still shows green.
type BackupStatusResponse struct {
	State        string     `json:"state"`
	Enabled      bool       `json:"enabled"`
	JobExists    bool       `json:"job_exists"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastStatus   string     `json:"last_status,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastSize     int64      `json:"last_size_bytes,omitempty"`
	AgeSeconds   int64      `json:"age_seconds,omitempty"`
}

// BackupStatus returns a compact status record for the sidebar pill
// and the Settings > System automated-backup section. Cheap enough to
// poll (single jobs scan + recent-runs query) and avoids exposing the
// full backup jobs/runs API to non-admin viewers.
func (h *Handlers) BackupStatus(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeJSON(w, http.StatusOK, BackupStatusResponse{State: "never"})
		return
	}
	st, err := h.Backups.LastSystemRun(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := BackupStatusResponse{Enabled: st.Enabled, JobExists: st.Exists}
	if !st.Exists {
		resp.State = "never"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !st.Enabled {
		resp.State = "disabled"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if st.Run == nil {
		resp.State = "never"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	run := st.Run
	ts := run.StartedAt
	if run.FinishedAt != nil {
		ts = *run.FinishedAt
	}
	resp.LastRunAt = &ts
	resp.LastStatus = run.Status
	resp.LastError = run.Error
	resp.LastSize = run.SizeBytes
	age := time.Since(ts)
	resp.AgeSeconds = int64(age.Seconds())
	switch {
	case run.Status != "success":
		resp.State = "failed"
	case age > 36*time.Hour:
		resp.State = "stale"
	default:
		resp.State = "ok"
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetBackupEnabledRequest toggles the default system backup job. This
// is a separate endpoint from the full jobs CRUD so the Settings page
// can bind a single switch without owning job IDs.
type SetBackupEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handlers) SetBackupEnabled(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups unavailable")
		return
	}
	var req SetBackupEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Backups.SetDefaultJobEnabled(r.Context(), req.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.BackupStatus(w, r)
}

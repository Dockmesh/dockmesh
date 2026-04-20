package handlers

import (
	"context"
	"encoding/json"
	"fmt"
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

// -----------------------------------------------------------------------
// /system/health — aggregated at-a-glance health for the sidebar-header
// HealthDot. One request, everything an admin needs to know "is
// anything burning?" without leaving the current page.
// -----------------------------------------------------------------------

// HealthCheck is one atomic fact about a subsystem. Severity follows
// the "traffic light" model everyone recognises:
//
//	ok   — green
//	warn — yellow (degraded, action optional)
//	fail — red (action required)
//	off  — grey (feature disabled; not a problem in itself)
type HealthCheck struct {
	Name    string `json:"name"`            // stable id: "backup" | "proxy" | "agents" | "disk" | "scanner"
	Label   string `json:"label"`           // human string for the popover row
	Status  string `json:"status"`          // ok | warn | fail | off
	Detail  string `json:"detail,omitempty"` // sub-line shown under label
	LinkTo  string `json:"link_to,omitempty"` // UI route to jump to on click
	Message string `json:"message,omitempty"` // tooltip (surfaces raw errors)
}

// HealthResponse is what /system/health returns. `overall` aggregates
// the worst status across all checks using the ranking
// fail > warn > off > ok so a single red check flips the dot.
type HealthResponse struct {
	Overall string        `json:"overall"`
	Checks  []HealthCheck `json:"checks"`
}

// SystemHealth aggregates the small status signals we already compute
// separately (backup, proxy, agents, disk, scanner) into one response
// so the sidebar's HealthDot can render everything from a single poll.
func (h *Handlers) SystemHealth(w http.ResponseWriter, r *http.Request) {
	checks := make([]HealthCheck, 0, 6)

	// ---- backup
	checks = append(checks, h.healthBackup(r.Context()))

	// ---- proxy
	if h.Proxy != nil {
		st := h.Proxy.GetStatus(r.Context())
		c := HealthCheck{Name: "proxy", Label: "Reverse proxy", LinkTo: "/proxy"}
		switch {
		case !st.Enabled:
			c.Status = "off"
			c.Detail = "disabled"
		case !st.Running:
			c.Status = "fail"
			c.Detail = "container not running"
		case !st.AdminOK:
			c.Status = "warn"
			c.Detail = "admin API unreachable"
		default:
			c.Status = "ok"
			c.Detail = "Caddy " + st.Version
		}
		checks = append(checks, c)
	}

	// ---- agents: online / total, warn if any offline
	if h.Agents != nil {
		ags, _ := h.Agents.List(r.Context())
		online := 0
		for _, a := range ags {
			if a.Status == "online" {
				online++
			}
		}
		c := HealthCheck{Name: "agents", Label: "Agents", LinkTo: "/agents"}
		switch {
		case len(ags) == 0:
			c.Status = "off"
			c.Detail = "no agents enrolled"
		case online == len(ags):
			c.Status = "ok"
			c.Detail = fmt.Sprintf("%d/%d online", online, len(ags))
		default:
			c.Status = "warn"
			c.Detail = fmt.Sprintf("%d/%d online", online, len(ags))
		}
		checks = append(checks, c)
	}

	// ---- disk: warn >80%, fail >95%, data directory is the critical one.
	if m := system.Collect(); m.DiskTotal > 0 {
		pct := m.DiskPercent
		c := HealthCheck{Name: "disk", Label: "Disk", LinkTo: "/"}
		c.Detail = fmt.Sprintf("%.0f%% used (%s)", pct, m.DiskPath)
		switch {
		case pct > 95:
			c.Status = "fail"
		case pct > 80:
			c.Status = "warn"
		default:
			c.Status = "ok"
		}
		checks = append(checks, c)
	}

	// ---- scanner (Grype). Binary presence gates the feature; if disabled
	// via settings we report "off".
	if h.Settings != nil {
		c := HealthCheck{Name: "scanner", Label: "CVE scanner", LinkTo: "/images"}
		if h.Settings.GetBool("scanner_enabled", false) {
			c.Status = "ok"
			c.Detail = "Grype ready"
		} else {
			c.Status = "off"
			c.Detail = "disabled"
		}
		checks = append(checks, c)
	}

	// Aggregate worst status: fail > warn > off > ok.
	rank := map[string]int{"ok": 0, "off": 1, "warn": 2, "fail": 3}
	overall := "ok"
	for _, c := range checks {
		if rank[c.Status] > rank[overall] {
			overall = c.Status
		}
	}
	writeJSON(w, http.StatusOK, HealthResponse{Overall: overall, Checks: checks})
}

// healthBackup adapts the existing BackupStatus flow into a HealthCheck
// so the aggregated /system/health endpoint doesn't need to duplicate
// its decision table.
func (h *Handlers) healthBackup(ctx context.Context) HealthCheck {
	c := HealthCheck{Name: "backup", Label: "System backup", LinkTo: "/settings?tab=system"}
	if h.Backups == nil {
		c.Status = "off"
		c.Detail = "backups service unavailable"
		return c
	}
	st, err := h.Backups.LastSystemRun(ctx)
	if err != nil {
		c.Status = "warn"
		c.Detail = "status query failed"
		c.Message = err.Error()
		return c
	}
	if !st.Exists {
		c.Status = "off"
		c.Detail = "no default job"
		return c
	}
	if !st.Enabled {
		c.Status = "off"
		c.Detail = "disabled"
		return c
	}
	if st.Run == nil {
		c.Status = "warn"
		c.Detail = "never run yet"
		return c
	}
	ts := st.Run.StartedAt
	if st.Run.FinishedAt != nil {
		ts = *st.Run.FinishedAt
	}
	age := time.Since(ts)
	switch {
	case st.Run.Status != "success":
		c.Status = "fail"
		c.Detail = "last run failed"
		c.Message = st.Run.Error
	case age > 36*time.Hour:
		c.Status = "warn"
		c.Detail = fmtAge(int64(age.Seconds())) + " ago (stale)"
	default:
		c.Status = "ok"
		c.Detail = fmtAge(int64(age.Seconds())) + " ago"
	}
	return c
}

// fmtAge is a tiny human-readable duration formatter for health
// detail strings. Mirrors the UI's fmtAge so the text lines up when
// a user compares a popover row with the page the LinkTo points at.
func fmtAge(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm", sec/60)
	}
	if sec < 86400 {
		return fmt.Sprintf("%dh", sec/3600)
	}
	return fmt.Sprintf("%dd", sec/86400)
}

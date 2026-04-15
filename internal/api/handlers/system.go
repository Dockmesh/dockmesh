package handlers

import (
	"context"
	"net/http"

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

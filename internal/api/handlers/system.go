package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/system"
)

// SystemMetrics returns a single host-metrics snapshot (CPU / memory /
// disk / uptime). Used by the dashboard "System health" panel.
func (h *Handlers) SystemMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, system.Collect())
}

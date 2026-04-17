package handlers

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PromMetrics serves the prometheus text exposition format. Wired at
// the router root (not /api/v1) so scrapers don't need to know the
// API version. Auth is handled by the router layer — the handler
// itself is agnostic.
func (h *Handlers) PromMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Prom == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics collector not configured")
		return
	}
	promhttp.HandlerFor(h.Prom.Registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

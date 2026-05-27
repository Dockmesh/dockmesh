package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/go-chi/chi/v5"
)

// BatchContainerStats returns the latest CPU + memory sample for
// every container that's reported in the last 60 seconds. UI uses
// this for the containers-list CPU/Mem columns — far cheaper than
// the per-container WebSocket stream the detail view uses.
//
//	GET /api/v1/hosts/{id}/stats/containers
//
// (the host id is accepted in the path for symmetry with the other
// per-host endpoints, but the metrics collector currently aggregates
// across the local docker daemon only — agent containers don't yet
// flow through the local metrics_raw table)
func (h *Handlers) BatchContainerStats(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	latest, err := h.Metrics.LatestPerContainer(r.Context(), 60)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, latest)
}

// GetMetrics returns historical samples for a container. The id parameter
// is resolved to a container name via InspectContainer so the URL can use
// the current id while the DB continues to key on name.
//
// Query params:
//   from        unix seconds (default: now - 1h)
//   to          unix seconds (default: now)
//   resolution  raw | 1m | 1h (default: raw)
func (h *Handlers) GetMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}

	id := chi.URLParam(r, "id")
	info, err := h.Docker.InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	name := strings.TrimPrefix(info.Name, "/")

	q := metrics.Query{Name: name, Resolution: r.URL.Query().Get("resolution")}
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.From = time.Unix(n, 0)
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.To = time.Unix(n, 0)
		}
	}

	samples, err := h.Metrics.Query(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, samples)
}

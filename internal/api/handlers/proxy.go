package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/go-chi/chi/v5"
)

type proxyRouteRequest struct {
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	TLSMode  string `json:"tls_mode"`
	// Enabled is *bool so PUT bodies that omit the field keep the
	// existing state; only an explicit false disables the route.
	Enabled  *bool  `json:"enabled,omitempty"`
}

func (h *Handlers) ProxyStatus(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	writeJSON(w, http.StatusOK, h.Proxy.GetStatus(r.Context()))
}

func (h *Handlers) ProxyEnable(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	if err := h.Proxy.EnableProxy(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.enable", "", nil)
	writeJSON(w, http.StatusOK, h.Proxy.GetStatus(r.Context()))
}

func (h *Handlers) ProxyDisable(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	if err := h.Proxy.DisableProxy(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.disable", "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// GetProxyMetrics scrapes Caddy's Prometheus /metrics endpoint and
// returns a per-host snapshot for the proxy dashboard. Returns an
// empty snapshot rather than 503 when Caddy doesn't expose metrics —
// the UI still renders the route list and just hides the charts.
func (h *Handlers) GetProxyMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	snap, err := h.Proxy.Metrics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// ListACMEEvents returns the recent ACME issuance / renewal events the
// log tailer has captured. Empty array when the proxy is disabled or
// no events have happened yet.
func (h *Handlers) ListACMEEvents(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeJSON(w, http.StatusOK, []proxy.ACMEEvent{})
		return
	}
	writeJSON(w, http.StatusOK, h.Proxy.ACMEEvents())
}

func (h *Handlers) ListProxyRoutes(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	routes, err := h.Proxy.ListRoutes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Probe each route's TLS to surface cert metadata (issuer +
	// validity window) so the UI can render expiry warnings. Bounded
	// to 6 seconds total — Caddy's local rate-limiting tolerates ~12
	// concurrent probes; we don't want a slow lookup to stall the
	// list response.
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	routes = h.Proxy.EnrichWithCertInfo(ctx, routes)
	writeJSON(w, http.StatusOK, routes)
}

func (h *Handlers) CreateProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	var req proxyRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Host == "" || req.Upstream == "" {
		writeError(w, http.StatusBadRequest, "host and upstream required")
		return
	}
	if req.TLSMode == "" {
		req.TLSMode = "auto"
	}
	route, err := h.Proxy.CreateRoute(r.Context(), req.Host, req.Upstream, req.TLSMode)
	if errors.Is(err, proxy.ErrDuplicateHost) {
		writeError(w, http.StatusConflict, "host already has a route")
		return
	}
	if errors.Is(err, proxy.ErrInvalidTLSMode) {
		writeError(w, http.StatusBadRequest, "invalid tls_mode")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.route_create", req.Host, map[string]string{"upstream": req.Upstream, "tls_mode": req.TLSMode})
	writeJSON(w, http.StatusCreated, route)
}

func (h *Handlers) UpdateProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req proxyRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := h.Proxy.UpdateRoute(r.Context(), id, req.Upstream, req.TLSMode, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.route_update", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// SetProxyRouteEnabled flips just the enabled flag for one route. Used
// by the row-level toggle in the routes table — saves the caller from
// having to send back upstream + tls_mode unchanged just to disable a
// host. Body: `{"enabled": true|false}`.
//
//	PATCH /api/v1/proxy/routes/{id}/enabled
func (h *Handlers) SetProxyRouteEnabled(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.Proxy.SetRouteEnabled(r.Context(), id, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state := "enabled"
	if !body.Enabled {
		state = "disabled"
	}
	h.audit(r, "proxy.route_"+state, idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// GetProxyRouteMetrics returns the per-host slice of the current Caddy
// metrics snapshot for one route. Cheaper than fetching the full
// MetricsSnapshot when only one row needs to be refreshed (e.g. the
// expanded-route panel polls this every 5s).
//
//	GET /api/v1/proxy/routes/{id}/metrics
func (h *Handlers) GetProxyRouteMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	routes, err := h.Proxy.ListRoutes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var host string
	for _, rt := range routes {
		if rt.ID == id {
			host = rt.Host
			break
		}
	}
	if host == "" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	snap, err := h.Proxy.Metrics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, hm := range snap.PerHost {
		if hm.Host == host {
			writeJSON(w, http.StatusOK, hm)
			return
		}
	}
	// No metrics for this host yet — return an empty zero-value slice so
	// the caller renders "0 req/s" instead of a 404.
	writeJSON(w, http.StatusOK, map[string]any{
		"host":           host,
		"requests":       0,
		"requests_per_second": 0,
		"status_buckets": map[string]uint64{},
		"p95_latency_ms": 0,
	})
}

func (h *Handlers) DeleteProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Proxy.DeleteRoute(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.route_delete", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

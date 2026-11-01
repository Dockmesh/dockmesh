package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

type proxyRouteRequest struct {
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	TLSMode  string `json:"tls_mode"`
	// Enabled is *bool so PUT bodies that omit the field keep the
	// existing state.
	Enabled *bool `json:"enabled,omitempty"`
	// Options stay raw so an omitted object (keep on update) can be
	// told apart from an empty one (defaults).
	Options json.RawMessage `json:"options,omitempty"`
}

// maxProxyRouteBody bounds route create/update bodies; options are small.
const maxProxyRouteBody = 256 << 10

// writeProxyRouteError maps validation errors of route writes to 4xx.
// It reports false when err is not one of them.
func writeProxyRouteError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, proxy.ErrDuplicateHost):
		writeError(w, http.StatusConflict, "host already has a route")
	case errors.Is(err, proxy.ErrWWWConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, proxy.ErrRouteNotFound):
		writeError(w, http.StatusNotFound, "route not found")
	case errors.Is(err, proxy.ErrInvalidTLSMode):
		writeError(w, http.StatusBadRequest, "invalid tls_mode")
	case errors.Is(err, proxy.ErrInvalidUpstream), errors.Is(err, proxy.ErrAdminUpstream),
		errors.Is(err, proxy.ErrInvalidHost), errors.Is(err, proxy.ErrInvalidOptions):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		return false
	}
	return true
}

// orNone keeps an empty change list as [] in audit details.
func orNone(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// canEditProxy reports whether the caller may see header values and the
// full forward-auth URL; viewers get them masked.
func canEditProxy(r *http.Request) bool {
	return middleware.Allowed(r.Context(), rbac.PermProxyUpdate)
}

// persistProxyEnabled stores the operator's choice so it is applied again
// after a restart. A failed write is logged: the container is already in
// the requested state and the toggle can be repeated.
func (h *Handlers) persistProxyEnabled(r *http.Request, value string) {
	if h.Settings == nil {
		return
	}
	if err := h.Settings.Set(r.Context(), proxy.KeyEnabled, value); err != nil {
		slog.Error("proxy: saving proxy_enabled failed", "err", err)
	}
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
	h.persistProxyEnabled(r, "true")
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
	h.persistProxyEnabled(r, "false")
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
	if !canEditProxy(r) {
		for i := range routes {
			routes[i].Options = routes[i].Options.Masked()
		}
	}
	writeJSON(w, http.StatusOK, routes)
}

func (h *Handlers) CreateProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyRouteBody)
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
	opts, err := proxy.ParseOptions(req.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	started := time.Now()
	op := h.Operations.Start(r.Context(), operations.Spec{
		Kind:   opProxySite,
		Target: req.Host,
		Ref:    "proxy-site:" + req.Host,
		Actor:  opActor(r.Context()),
		Steps:  proxy.NewSiteSteps,
	})
	route, err := h.Proxy.CreateRoute(operations.WithOp(r.Context(), op), proxy.RouteSpec{
		Host: req.Host, Upstream: req.Upstream, TLSMode: req.TLSMode, Options: opts,
	})
	switch {
	case err != nil && route != nil:
		slog.Warn("proxy: new site saved but not applied", "host", route.Host, "err", err)
		op.Finish(proxy.ErrConfigRejected)
	case err != nil:
		op.Finish(err)
	default:
		go h.Proxy.WatchCertificate(context.WithoutCancel(r.Context()), op, route.Host, route.TLSMode, started)
	}
	if writeProxyRouteError(w, err) {
		return
	}
	if err != nil && route != nil {
		writeError(w, http.StatusInternalServerError, proxy.ErrConfigRejected.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	details := map[string]any{"upstream": req.Upstream, "tls_mode": req.TLSMode}
	if opts != nil {
		details["options"] = orNone(proxy.ChangedGroups(proxy.SiteOptions{}, *opts))
	}
	h.audit(r, "proxy.route_create", req.Host, details)
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
	r.Body = http.MaxBytesReader(w, r.Body, maxProxyRouteBody)
	var req proxyRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	opts, err := proxy.ParseOptions(req.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	changed, err := h.Proxy.UpdateRoute(r.Context(), id, req.Upstream, req.TLSMode, req.Enabled, opts)
	if changed != nil || err == nil {
		h.audit(r, "proxy.route_update", idStr, map[string]any{"changed": orNone(changed)})
	}
	if writeProxyRouteError(w, err) {
		return
	}
	if err != nil && changed != nil {
		slog.Warn("proxy: site saved but not applied", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, proxy.ErrConfigRejected.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	route, err := h.Proxy.GetRoute(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, route)
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
	err = h.Proxy.SetRouteEnabled(r.Context(), id, body.Enabled)
	if writeProxyRouteError(w, err) {
		return
	}
	if err != nil {
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
		"host":                host,
		"requests":            0,
		"requests_per_second": 0,
		"status_buckets":      map[string]uint64{},
		"p95_latency_ms":      0,
	})
}

// GetProxyRouteConfig returns the Caddy config Dockmesh generates for
// one site, read-only and with basic-auth hashes redacted.
//
//	GET /api/v1/proxy/routes/{id}/config
func (h *Handlers) GetProxyRouteConfig(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	preview, err := h.Proxy.SiteConfig(r.Context(), id, !canEditProxy(r))
	if errors.Is(err, proxy.ErrRouteNotFound) {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
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

// ImportProxyCaddyfile maps the sites of a Caddyfile to routes with
// options. Importable sites are created disabled unless dry_run is set;
// what can't be mapped is reported per site.
//
//	POST /api/v1/proxy/import
func (h *Handlers) ImportProxyCaddyfile(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*proxy.MaxCaddyfileSize)
	var req struct {
		Caddyfile string `json:"caddyfile"`
		DryRun    bool   `json:"dry_run"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Caddyfile) == "" {
		writeError(w, http.StatusBadRequest, "caddyfile required")
		return
	}
	res, err := h.Proxy.ImportCaddyfile(r.Context(), req.Caddyfile, req.DryRun)
	switch {
	case errors.Is(err, proxy.ErrProxyNotConfigured):
		writeError(w, http.StatusConflict, "start the proxy first: Caddy reads the Caddyfile")
		return
	case errors.Is(err, proxy.ErrCaddyfileRejected), errors.Is(err, proxy.ErrCaddyfileInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var created []string
	for _, s := range res.Sites {
		if s.RouteID != 0 {
			created = append(created, s.Host)
		}
	}
	h.audit(r, "proxy.import", "", map[string]any{"dry_run": req.DryRun, "created": created, "sites": len(res.Sites)})
	writeJSON(w, http.StatusOK, res)
}

// GetProxyVersion reports the running Caddy version and the tested
// versions this release can switch to.
//
//	GET /api/v1/proxy/version
func (h *Handlers) GetProxyVersion(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	info := h.Proxy.VersionInfo()
	// A custom image can name a private registry.
	if !info.Managed && !canEditProxy(r) {
		info.Image = ""
	}
	writeJSON(w, http.StatusOK, info)
}

// SetProxyVersion switches Caddy to another tested version; the switch
// is undone when the new container doesn't take the config.
//
//	PUT /api/v1/proxy/version
func (h *Handlers) SetProxyVersion(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ch, err := h.Proxy.SwitchVersion(r.Context(), req.Version)
	h.writeVersionSwitch(w, r, "proxy.version_update", ch, err)
}

// RollbackProxyVersion switches back to the version before the last
// switch.
//
//	POST /api/v1/proxy/version/rollback
func (h *Handlers) RollbackProxyVersion(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	ch, err := h.Proxy.RollbackVersion(r.Context())
	h.writeVersionSwitch(w, r, "proxy.version_rollback", ch, err)
}

// writeVersionSwitch audits detached from the request, which may have
// been cancelled while the container was replaced.
func (h *Handlers) writeVersionSwitch(w http.ResponseWriter, r *http.Request, action string, ch proxy.VersionChange, err error) {
	switch {
	case errors.Is(err, proxy.ErrVersionReadOnly), errors.Is(err, proxy.ErrProxyNotRunning):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, proxy.ErrUnknownVersion), errors.Is(err, proxy.ErrNoPreviousVersion):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, proxy.ErrVersionSwitchFailed):
		result := "rolled back"
		if errors.Is(err, proxy.ErrVersionRollbackFailed) {
			result = "rollback failed"
		}
		h.auditDetached(r, action, ch.To, map[string]string{"from": ch.From, "to": ch.To, "result": result, "error": err.Error()})
		writeError(w, http.StatusBadGateway, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ch.From != ch.To {
		h.auditDetached(r, action, ch.To, map[string]string{"from": ch.From, "to": ch.To})
	}
	writeJSON(w, http.StatusOK, ch.Info)
}

// GetProxySettings returns the proxy-wide options. Without proxy.update
// the ACME email is shown with its local part hidden.
//
//	GET /api/v1/proxy/settings
func (h *Handlers) GetProxySettings(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	st := h.Proxy.ProxySettings()
	if !canEditProxy(r) {
		st = st.Masked()
	}
	writeJSON(w, http.StatusOK, st)
}

// UpdateProxySettings stores and applies the proxy-wide options.
//
//	PUT /api/v1/proxy/settings
func (h *Handlers) UpdateProxySettings(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy not configured")
		return
	}
	var req proxy.Settings
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	before := h.Proxy.ProxySettings()
	err := h.Proxy.SaveSettings(r.Context(), req)
	if errors.Is(err, proxy.ErrInvalidSettings) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after := h.Proxy.ProxySettings()
	h.auditDetached(r, "proxy.settings_update", "", map[string]any{"before": before, "after": after})
	writeJSON(w, http.StatusOK, after)
}

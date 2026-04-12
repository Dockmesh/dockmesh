package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/go-chi/chi/v5"
)

type proxyRouteRequest struct {
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	TLSMode  string `json:"tls_mode"`
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
	if err := h.Proxy.UpdateRoute(r.Context(), id, req.Upstream, req.TLSMode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "proxy.route_update", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
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

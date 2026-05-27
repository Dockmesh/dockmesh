package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	dtypes "github.com/docker/docker/api/types"
	"github.com/go-chi/chi/v5"
)

// containerRow is the all-mode row for ListContainers. The embedded
// dtypes.Container marshals its fields flat alongside host_id / host_name
// so the frontend reads everything off one JSON object instead of having
// to follow a .row indirection.
type containerRow struct {
	dtypes.Container
	HostID       string `json:"host_id"`
	HostName     string `json:"host_name"`
	HealthStatus string `json:"health_status,omitempty"` // healthy / unhealthy / starting / "" (none)
}

// parseHealthFromStatus extracts the healthcheck verdict from Docker's
// Status string. Docker formats it as e.g. "Up 5 minutes (healthy)",
// "Up 5 minutes (unhealthy)", "Up 5 minutes (health: starting)" — or
// leaves the parens out entirely when the container has no healthcheck.
// Returning a structured enum lets the UI stop string-parsing.
func parseHealthFromStatus(status string) string {
	open := strings.IndexByte(status, '(')
	close := strings.IndexByte(status, ')')
	if open < 0 || close < 0 || close <= open {
		return ""
	}
	inner := strings.ToLower(strings.TrimSpace(status[open+1 : close]))
	switch {
	case strings.HasPrefix(inner, "health: starting"), inner == "starting":
		return "starting"
	case inner == "healthy":
		return "healthy"
	case inner == "unhealthy":
		return "unhealthy"
	default:
		return ""
	}
}

func (h *Handlers) ListContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	hostID := r.URL.Query().Get("host")

	// All-mode: fan out across every online host, collect rows with per-
	// row host metadata, report unreachable hosts in a structured way so
	// the frontend can show "Showing data from N of M hosts".
	if host.IsAll(hostID) {
		if h.Hosts == nil {
			// No registry wired — legacy single-host boot. Degrade to local.
			hostID = "local"
		} else {
			targets := h.Hosts.PickAll(r.Context())
			targets = h.filterHostsByScope(r, targets)
			res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]containerRow, error) {
				list, err := hh.ListContainers(ctx, all)
				if err != nil {
					return nil, err
				}
				rows := make([]containerRow, len(list))
				for i, c := range list {
					rows[i] = containerRow{
						Container:    c,
						HostID:       hh.ID(),
						HostName:     hh.Name(),
						HealthStatus: parseHealthFromStatus(c.Status),
					}
				}
				return rows, nil
			})
			// Flatten + filter, then re-pack into the FanOutResult shape.
			res.Items = h.filterContainerRowsByRoleScope(r, res.Items)
			writeJSON(w, http.StatusOK, res)
			return
		}
	}

	// Single-host path — unchanged shape, unchanged single-host callers.
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	list, err := target.ListContainers(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list = h.filterContainersByRoleScope(r, target.ID(), list)
	// Enrich each row with the parsed health status so the frontend
	// doesn't need its own regex on the Docker Status string. The
	// embedded dtypes.Container marshals flat, so adding a sibling
	// JSON field is non-breaking for existing single-host callers.
	enriched := make([]struct {
		dtypes.Container
		HealthStatus string `json:"health_status,omitempty"`
	}, len(list))
	for i, c := range list {
		enriched[i].Container = c
		enriched[i].HealthStatus = parseHealthFromStatus(c.Status)
	}
	writeJSON(w, http.StatusOK, enriched)
}

func (h *Handlers) InspectContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	info, err := target.InspectContainer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handlers) StartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "start", audit.ActionContainerStart)
}

func (h *Handlers) StopContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "stop", audit.ActionContainerStop)
}

func (h *Handlers) RestartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "restart", audit.ActionContainerKill)
}

// PauseContainer freezes a running container's processes via the freezer
// cgroup. Data is preserved in memory — useful for incident response.
//
//	POST /api/v1/containers/:id/pause
func (h *Handlers) PauseContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "pause", "container.pause")
}

// UnpauseContainer resumes a paused container.
//
//	POST /api/v1/containers/:id/unpause
func (h *Handlers) UnpauseContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "unpause", "container.unpause")
}

// KillContainer sends a signal to the container's main process. Body
// may specify {"signal": "SIGKILL"}; empty/missing body defaults to
// SIGKILL (Docker's default). Separate from the generic action helper
// because it takes a body + derives the signal label for audit.
//
//	POST /api/v1/containers/:id/kill
func (h *Handlers) KillContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersUpdate, scopeReq, "container "+id)
		return
	}
	var body struct {
		Signal string `json:"signal"`
	}
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &body)
	}
	if err := target.KillContainer(r.Context(), id, body.Signal); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	signalStr := body.Signal
	if signalStr == "" {
		signalStr = "SIGKILL"
	}
	h.audit(r, "container.kill", id, map[string]string{
		"host":   target.ID(),
		"signal": signalStr,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersDelete, scopeReq, "container "+id)
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := target.RemoveContainer(r.Context(), id, force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRm, id, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}

// containerAction dispatches a start/stop/restart op against whichever host
// (local or agent) the request is targeted at via ?host=.
func (h *Handlers) containerAction(w http.ResponseWriter, r *http.Request, op string, action string) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersUpdate, scopeReq, "container "+id)
		return
	}
	switch op {
	case "start":
		err = target.StartContainer(r.Context(), id)
	case "stop":
		err = target.StopContainer(r.Context(), id)
	case "restart":
		err = target.RestartContainer(r.Context(), id)
	case "pause":
		err = target.PauseContainer(r.Context(), id)
	case "unpause":
		err = target.UnpauseContainer(r.Context(), id)
	default:
		err = errors.New("unknown op: " + op)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, action, id, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}

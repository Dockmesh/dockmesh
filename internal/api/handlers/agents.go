package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/webtls"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListAgents(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	list, err := h.Agents.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	visible := make([]agents.Agent, 0, len(list))
	for _, a := range list {
		if h.hostVisible(r, a.ID) {
			visible = append(visible, a)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if !h.hostVisible(r, id) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a, err := h.Agents.Get(r.Context(), id)
	if errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type createAgentReq struct {
	Name      string   `json:"name"`
	Tags      []string `json:"tags"`
	StacksDir string   `json:"stacks_dir"`
}

func (h *Handlers) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	var req createAgentReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	tags, err := hosttags.Canonicalize(req.Tags)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, hosttags.ErrTooManyTags) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	if len(tags) > 0 {
		// Tags carry RBAC scope; setting them needs hosts.tag as
		// PUT /hosts/{id}/tags does.
		if !middleware.Allowed(r.Context(), rbac.PermHostsTag) {
			middleware.Forbidden(w, r, rbac.PermHostsTag)
			return
		}
		if h.HostTags == nil {
			writeError(w, http.StatusServiceUnavailable, "host tags unavailable")
			return
		}
	}
	res, err := h.Agents.CreateWith(r.Context(), agents.CreateOptions{Name: req.Name, StacksDir: req.StacksDir})
	switch {
	case errors.Is(err, agents.ErrNameTaken):
		writeError(w, http.StatusConflict, "name already in use")
		return
	case errors.Is(err, agents.ErrInvalidStacksDir):
		writeError(w, http.StatusBadRequest, msgInvalidStacksDir)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(tags) > 0 {
		if _, err := h.HostTags.Set(r.Context(), res.Agent.ID, tags); err != nil {
			// The host exists without tags; remove it so a retry with
			// the same name works.
			if derr := h.Agents.Delete(r.Context(), res.Agent.ID); derr != nil {
				slog.Warn("remove host after failed tags", "id", res.Agent.ID, "err", derr)
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.audit(r, audit.ActionAgentCreate, "agent:"+res.Agent.Name, map[string]any{"tags": tags, "stacks_dir": req.StacksDir})
	res.BehindProxy = h.ownReverseProxy()
	writeJSON(w, http.StatusCreated, res)
}

// CheckAgentReachability dials the agent URL from this server and reports
// whether an agent could enroll and connect through it.
//
//	POST /api/v1/agents/reachability
func (h *Handlers) CheckAgentReachability(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	res, err := h.Agents.TryCheckReachability(r.Context())
	if errors.Is(err, agents.ErrCheckRunning) {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.hostScopeReq(id)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsDelete, scopeReq, "host "+id)
		return
	}
	if !h.requireHostAccess(w, r, id) {
		return
	}
	if err := h.Agents.Delete(r.Context(), id); errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Force-close the live WebSocket so a deleted agent can't continue
	// holding an authenticated session. Safe to call if not connected.
	h.Agents.Disconnect(id)
	// Drop any tags the host had — they're meaningless now and the
	// in-memory cache would keep them alive otherwise.
	if h.HostTags != nil {
		if err := h.HostTags.RemoveAllForHost(r.Context(), id); err != nil {
			slog.Warn("host tags cleanup after agent delete", "id", id, "err", err)
		}
	}
	h.audit(r, audit.ActionAgentDelete, "agent:"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// RotateAgentEnrollToken mints a fresh one-time token for an existing
// agent, invalidating the old one. Response shape matches CreateAgent
// so the UI can reuse the install-command modal.
//
//	POST /api/v1/agents/{id}/rotate-token
func (h *Handlers) RotateAgentEnrollToken(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	// A rotated token re-enrolls the host under the same ID, so it is
	// as sensitive as replacing the host and needs the same scope.
	scopeReq := h.hostScopeReq(id)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsCreate, scopeReq, "host "+id)
		return
	}
	if !h.requireHostAccess(w, r, id) {
		return
	}
	res, err := h.Agents.RotateEnrollToken(r.Context(), id)
	if errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "agent.rotate_token", "agent:"+id, nil)
	res.BehindProxy = h.ownReverseProxy()
	writeJSON(w, http.StatusOK, res)
}

// UpgradeAgent pushes a self-upgrade request to a connected agent.
// The agent downloads the new binary from the server's /install/ endpoint
// and restarts via systemd.
//
//	POST /api/v1/agents/{id}/upgrade
func (h *Handlers) UpgradeAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.hostScopeReq(id)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsUpdate, scopeReq, "host "+id)
		return
	}
	if !h.requireHostAccess(w, r, id) {
		return
	}
	if h.AgentUpgrade == nil {
		writeError(w, http.StatusServiceUnavailable, "agent upgrade controller not configured")
		return
	}
	req, err := h.AgentUpgrade.UpgradeOne(r.Context(), id)
	if errors.Is(err, agents.ErrAgentNotConnected) {
		writeError(w, http.StatusServiceUnavailable, "agent not connected")
		return
	}
	if errors.Is(err, agents.ErrAgentCurrent) {
		writeError(w, http.StatusConflict, "this host's agent already runs the same build as the server; there is nothing to upgrade")
		return
	}
	if errors.Is(err, agents.ErrAgentUpgrading) {
		writeError(w, http.StatusConflict, "this host's agent is still installing an earlier upgrade; try again once it has reconnected")
		return
	}
	if errors.Is(err, agents.ErrHostBusy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, agents.ErrNoAgentBinary) || errors.Is(err, agents.ErrNoRelease) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		// An agent too old to know the upgrade frame returns
		// "unknown request type: req.agent.upgrade" — surface this as
		// a 422 with an actionable hint rather than a bare 500, since
		// the fix is a manual one-time install, not a server bug.
		msg := err.Error()
		if strings.Contains(msg, "unknown request type") && strings.Contains(msg, "req.agent.upgrade") {
			writeError(w, http.StatusUnprocessableEntity,
				"agent version too old to self-upgrade — run the install script on the host once ("+
					h.Agents.PublicURL()+"/install/agent.sh with a fresh enrollment token), then future upgrades will work from the UI")
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	h.audit(r, audit.ActionAgentUpgrade, id, map[string]any{
		"action":  "agent-upgrade",
		"version": req.Version,
		"binary":  path.Base(req.BinaryURL),
		"sha256":  req.SHA256,
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "upgrading",
		"version": req.Version,
	})
}

type setStacksDirReq struct {
	Path string `json:"path"`
}

// SetHostStacksDir sets where new stacks go on a connected host. The
// directory the agent uses now is confirmed at once (200), which also
// ends "Waiting for setup"; another path goes to the agent's root
// updater (202) and its answer shows as stacks_dir in GET /agents/{id}.
//
//	PUT /api/v1/hosts/{id}/stacks-dir
func (h *Handlers) SetHostStacksDir(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.hostScopeReq(id)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsUpdate, scopeReq, "host "+id)
		return
	}
	if !h.requireHostAccess(w, r, id) {
		return
	}
	var req setStacksDirReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, err := h.Agents.Get(r.Context(), id); errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	applied, err := h.Agents.SetStacksDir(r.Context(), id, req.Path)
	switch {
	case errors.Is(err, agents.ErrInvalidStacksDir):
		writeError(w, http.StatusBadRequest, msgInvalidStacksDir)
		return
	case errors.Is(err, agents.ErrAgentNotConnected):
		writeError(w, http.StatusConflict, "agent not connected")
		return
	case errors.Is(err, agents.ErrLegacyLayout), errors.Is(err, agents.ErrApplyRunning):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "host.stacks_dir_set", "agent:"+id, map[string]any{"path": req.Path, "applied": applied})
	a, err := h.Agents.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusAccepted
	if applied {
		status = http.StatusOK
	}
	writeJSON(w, status, a)
}

// ownReverseProxy: the admin's proxy serves the web UI, which agents
// can't use to reach the agent port.
func (h *Handlers) ownReverseProxy() bool {
	return h.RestoreConfig != nil && webtls.Mode(h.RestoreConfig.TLSMode) == webtls.Proxy
}

// msgInvalidStacksDir is the setup page's text for a path the server
// refuses before asking the agent.
const msgInvalidStacksDir = "Enter an absolute path, for example /srv/dockmesh/stacks."

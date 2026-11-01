package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
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
		if h.agentInScope(r, a.ID) {
			visible = append(visible, a)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

// agentInScope applies both the user's scope tags and the role scope to
// the host an agent stands for, the same rules /hosts filters by.
func (h *Handlers) agentInScope(r *http.Request, id string) bool {
	return h.canAccessHost(r, id) && h.checkRoleScope(r, h.hostScopeReq(r.Context(), id))
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if !h.agentInScope(r, id) {
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
	Name string `json:"name"`
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
	res, err := h.Agents.Create(r.Context(), req.Name)
	if errors.Is(err, agents.ErrNameTaken) {
		writeError(w, http.StatusConflict, "name already in use")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackCreate, "agent:"+res.Agent.Name, nil)
	writeJSON(w, http.StatusCreated, res)
}

func (h *Handlers) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.hostScopeReq(r.Context(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermHostsDelete, scopeReq, "host "+id)
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
	h.audit(r, audit.ActionStackDelete, "agent:"+id, nil)
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
	scopeReq := h.hostScopeReq(r.Context(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermHostsCreate, scopeReq, "host "+id)
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
	scopeReq := h.hostScopeReq(r.Context(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermHostsUpdate, scopeReq, "host "+id)
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
	if errors.Is(err, agents.ErrNoAgentBinary) {
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
	h.audit(r, audit.ActionStackDeploy, id, map[string]any{
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

// EnrollAgent is the public endpoint the agent binary calls during its
// first boot to swap a one-time token for a client cert. There is NO
// JWT / Bearer auth on this route — the token IS the auth.
func (h *Handlers) EnrollAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	var req agents.EnrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	resp, err := h.Agents.Enroll(r.Context(), req)
	if errors.Is(err, agents.ErrInvalidToken) {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

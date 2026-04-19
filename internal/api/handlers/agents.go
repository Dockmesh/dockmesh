package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/pkg/version"
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
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return
	}
	a, err := h.Agents.Get(r.Context(), chi.URLParam(r, "id"))
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
	if err := h.Agents.Delete(r.Context(), id); errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	ag := h.Agents.GetConnected(id)
	if ag == nil {
		writeError(w, http.StatusServiceUnavailable, "agent not connected")
		return
	}

	// Determine the correct binary for the agent's architecture.
	// The agent's arch comes from the hello payload.
	agent, err := h.Agents.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	arch := agent.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	binaryName := "dockmesh-agent-linux-" + arch

	// Build the download URL from the server's base URL.
	baseURL := h.Agents.PublicURL()
	binaryURL := baseURL + "/install/" + binaryName

	req := agents.AgentUpgradeReq{
		BinaryURL: binaryURL,
		Version:   version.Version,
	}
	payload, _ := json.Marshal(req)
	resp, err := ag.Request(r.Context(), agents.Frame{
		Type:    agents.FrameReqAgentUpgrade,
		Payload: payload,
	})
	if err != nil {
		// An agent too old to know the upgrade frame returns
		// "unknown request type: req.agent.upgrade" — surface this as
		// a 422 with an actionable hint rather than a bare 500, since
		// the fix is a manual one-time install, not a server bug.
		msg := err.Error()
		if strings.Contains(msg, "unknown request type") && strings.Contains(msg, "req.agent.upgrade") {
			writeError(w, http.StatusUnprocessableEntity,
				"agent version too old to self-upgrade — run the install script on the host once ("+
					baseURL+"/install/agent.sh with a fresh enrollment token), then future upgrades will work from the UI")
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	if !resp.OK {
		writeError(w, http.StatusInternalServerError, resp.Error)
		return
	}
	h.audit(r, audit.ActionStackDeploy, id, map[string]any{
		"action":  "agent-upgrade",
		"version": version.Version,
		"binary":  binaryName,
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "upgrading",
		"version": version.Version,
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

package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
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
	h.audit(r, audit.ActionStackDelete, "agent:"+id, nil)
	w.WriteHeader(http.StatusNoContent)
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

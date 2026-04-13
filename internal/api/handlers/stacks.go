package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

type stackRequest struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
}

func (h *Handlers) ListStacks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Stacks.List())
}

func (h *Handlers) GetStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	d, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) CreateStack(w http.ResponseWriter, r *http.Request) {
	var req stackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Compose == "" {
		writeError(w, http.StatusBadRequest, "compose required")
		return
	}
	d, err := h.Stacks.Create(req.Name, req.Compose, req.Env)
	if err != nil {
		writeStackError(w, err)
		return
	}
	h.audit(r, audit.ActionStackCreate, req.Name, nil)
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handlers) UpdateStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req stackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Compose == "" {
		writeError(w, http.StatusBadRequest, "compose required")
		return
	}
	d, err := h.Stacks.Update(name, req.Compose, req.Env)
	if err != nil {
		writeStackError(w, err)
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, nil)
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) DeleteStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.Stacks.Delete(name); err != nil {
		writeStackError(w, err)
		return
	}
	h.audit(r, audit.ActionStackDelete, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) DeployStack(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	// Always read the canonical compose+env from the central server's
	// filesystem (where stacks live). The host abstraction takes the
	// content as parameters so local + remote share the same call shape.
	detail, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	res, err := target.DeployStack(r.Context(), name, detail.Compose, detail.Env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"services": len(res.Services),
		"host":     target.ID(),
	})
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) StopStack(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	if err := target.StopStack(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackStop, name, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) StackStatus(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	status, err := target.StackStatus(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func writeStackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, stacks.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, stacks.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, stacks.ErrInvalidName),
		errors.Is(err, stacks.ErrReserved),
		errors.Is(err, stacks.ErrPathEscape):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

package handlers

import (
	"context"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListContainers(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	all := r.URL.Query().Get("all") == "true"
	list, err := target.ListContainers(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
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
	h.containerAction(w, r, h.Docker.StartContainer, audit.ActionContainerStart)
}

func (h *Handlers) StopContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, h.Docker.StopContainer, audit.ActionContainerStop)
}

func (h *Handlers) RestartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, h.Docker.RestartContainer, audit.ActionContainerKill)
}

func (h *Handlers) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"
	if err := h.Docker.RemoveContainer(r.Context(), id, force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRm, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) containerAction(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) error, action string) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if err := fn(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, action, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

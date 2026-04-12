package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListContainers(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	all := r.URL.Query().Get("all") == "true"
	list, err := h.Docker.ListContainers(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) InspectContainer(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	info, err := h.Docker.InspectContainer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handlers) StartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, h.Docker.StartContainer)
}

func (h *Handlers) StopContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, h.Docker.StopContainer)
}

func (h *Handlers) RestartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, h.Docker.RestartContainer)
}

func (h *Handlers) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := h.Docker.RemoveContainer(r.Context(), chi.URLParam(r, "id"), force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) containerAction(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) error) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	if err := fn(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

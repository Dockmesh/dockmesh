package handlers

import (
	"errors"
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
	h.containerAction(w, r, "start", audit.ActionContainerStart)
}

func (h *Handlers) StopContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "stop", audit.ActionContainerStop)
}

func (h *Handlers) RestartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "restart", audit.ActionContainerKill)
}

func (h *Handlers) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
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
	id := chi.URLParam(r, "id")
	switch op {
	case "start":
		err = target.StartContainer(r.Context(), id)
	case "stop":
		err = target.StopContainer(r.Context(), id)
	case "restart":
		err = target.RestartContainer(r.Context(), id)
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

package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// GetDeployProgress returns the live state of an in-flight (or
// recently-finished) deploy for a stack so the UI can render a
// phase/service/elapsed indicator instead of a static "deploying…"
// pill. Returns 200 with a `null` body when no deploy is tracked.
//
//	GET /api/v1/stacks/{name}/deploy/progress
func (h *Handlers) GetDeployProgress(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if h.DeployTracker == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	state := h.DeployTracker.Get(name)
	writeJSON(w, http.StatusOK, state)
}

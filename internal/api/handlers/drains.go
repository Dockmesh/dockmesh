package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/go-chi/chi/v5"
)

// PlanDrain generates a bin-packing plan without executing.
//
//	POST /api/v1/hosts/{id}/drain/plan
func (h *Handlers) PlanDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	hostID := chi.URLParam(r, "id")
	plan, err := h.Drains.Plan(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// ExecuteDrain runs a drain plan.
//
//	POST /api/v1/hosts/{id}/drain/execute
func (h *Handlers) ExecuteDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	hostID := chi.URLParam(r, "id")
	userID := middleware.UserID(r.Context())
	d, err := h.Drains.Execute(r.Context(), hostID, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, hostID, map[string]any{
		"action":   "drain",
		"drain_id": d.ID,
		"stacks":   len(d.Plan),
	})
	writeJSON(w, http.StatusAccepted, d)
}

// GetDrain returns the current state of a drain.
//
//	GET /api/v1/hosts/{id}/drain/{drain_id}
func (h *Handlers) GetDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	d, err := h.Drains.Get(r.Context(), drainID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// PauseDrain pauses a running drain between stacks.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/pause
func (h *Handlers) PauseDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.PauseDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// ResumeDrain resumes a paused drain.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/resume
func (h *Handlers) ResumeDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.ResumeDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// AbortDrain cancels pending stacks in a drain.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/abort
func (h *Handlers) AbortDrain(w http.ResponseWriter, r *http.Request) {
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.AbortDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, chi.URLParam(r, "id"), map[string]any{
		"action":   "drain-abort",
		"drain_id": drainID,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

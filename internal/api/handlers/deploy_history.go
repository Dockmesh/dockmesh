package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

// ListDeployHistory returns the saved deploy snapshots for a stack,
// newest first. Compose YAML is NOT included — lightweight for the
// initial render; callers open a specific entry to see the YAML diff.
//
//	GET /api/v1/stacks/{name}/deployments
//
// P.12.6.
func (h *Handlers) ListDeployHistory(w http.ResponseWriter, r *http.Request) {
	if h.DeployHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "deploy history store not configured")
		return
	}
	name := chi.URLParam(r, "name")
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := h.DeployHistory.List(r.Context(), name, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// GetDeployHistoryEntry returns a single past deploy with its full
// compose YAML and the resolved services list. Used by the UI to let
// the operator confirm the target before rolling back.
//
//	GET /api/v1/stacks/{name}/deployments/{id}
func (h *Handlers) GetDeployHistoryEntry(w http.ResponseWriter, r *http.Request) {
	if h.DeployHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "deploy history store not configured")
		return
	}
	name := chi.URLParam(r, "name")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	entry, err := h.DeployHistory.Get(r.Context(), name, id)
	if err != nil {
		if errors.Is(err, stacks.ErrHistoryNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// RollbackToDeployment overwrites the stack's compose.yaml with the
// compose snapshot from the given history row and re-runs Deploy.
// Env is deliberately kept as-is so current secrets aren't clobbered.
//
//	POST /api/v1/stacks/{name}/deployments/{id}/rollback
//
// The rollback itself creates a fresh history row so operators can see
// "rolled back to v12" in the list and, if the rollback causes a new
// problem, roll forward again. P.12.6.
func (h *Handlers) RollbackToDeployment(w http.ResponseWriter, r *http.Request) {
	if h.DeployHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "deploy history store not configured")
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	name := chi.URLParam(r, "name")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	entry, err := h.DeployHistory.Get(r.Context(), name, id)
	if err != nil {
		if errors.Is(err, stacks.ErrHistoryNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Read current env so rollback only swings compose.yaml — secrets stay put.
	current, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}

	if _, err := h.Stacks.Update(name, entry.ComposeYAML, current.Env); err != nil {
		writeError(w, http.StatusInternalServerError, "update stack files: "+err.Error())
		return
	}

	// Re-deploy with the restored compose.
	res, err := target.DeployStack(r.Context(), name, entry.ComposeYAML, current.Env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "redeploy failed: "+err.Error()+
			". The stack's compose.yaml has already been overwritten with the rollback target; inspect the stack and either retry or roll forward.")
		return
	}
	if h.Deployments != nil {
		if err := h.Deployments.Set(r.Context(), name, target.ID(), "deployed"); err != nil {
			slog.Warn("set stack deployment after rollback", "stack", name, "err", err)
		}
	}

	// Record the rollback itself as a new history row so the list
	// shows "rolled back to #ID" as a first-class event.
	note := "rollback to #" + strconv.FormatInt(entry.ID, 10)
	services := make([]stacks.DeployHistoryService, 0, len(res.Services))
	for _, s := range res.Services {
		services = append(services, stacks.DeployHistoryService{Service: s.Name, Image: s.Image})
	}
	if _, err := h.DeployHistory.Record(r.Context(), name, target.ID(), entry.ComposeYAML, note, middleware.UserID(r.Context()), services); err != nil {
		slog.Warn("record rollback history", "stack", name, "err", err)
	}

	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":         "rollback",
		"rollback_to_id": entry.ID,
		"host":           target.ID(),
	})
	slog.Info("rollback",
		"stack", name, "target_id", entry.ID, "host", target.ID())
	writeJSON(w, http.StatusOK, map[string]any{
		"rolled_back_to":   entry.ID,
		"deployed_at_orig": entry.DeployedAt,
		"result":           res,
	})
}

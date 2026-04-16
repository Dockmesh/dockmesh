package handlers

import (
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/go-chi/chi/v5"
)

// InitiateMigration starts a new stack migration.
//
//	POST /api/v1/stacks/{name}/migrate { "target_host_id": "xxx" }
func (h *Handlers) InitiateMigration(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	var req migration.MigrateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.TargetHostID == "" {
		writeError(w, http.StatusBadRequest, "target_host_id required")
		return
	}
	userID := middleware.UserID(r.Context())
	m, err := h.Migrations.Initiate(r.Context(), name, req.TargetHostID, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":      "migrate",
		"migration_id": m.ID,
		"source":      m.SourceHostID,
		"target":      m.TargetHostID,
	})
	writeJSON(w, http.StatusAccepted, m)
}

// GetMigration returns the current state of a migration.
//
//	GET /api/v1/stacks/{name}/migrate/{id}
func (h *Handlers) GetMigration(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	m, err := h.Migrations.Store().Get(r.Context(), id)
	if err != nil {
		if err == migration.ErrNotFound {
			writeError(w, http.StatusNotFound, "migration not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// RollbackMigration triggers a manual rollback.
//
//	POST /api/v1/stacks/{name}/migrate/{id}/rollback
func (h *Handlers) RollbackMigration(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Migrations.Rollback(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":       "migrate-rollback",
		"migration_id": id,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rollback initiated"})
}

// ListMigrations returns all migrations, newest first.
//
//	GET /api/v1/migrations?limit=100
func (h *Handlers) ListMigrations(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	list, err := h.Migrations.Store().ListAll(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*migration.Migration{}
	}
	writeJSON(w, http.StatusOK, list)
}

// ListActiveMigrations returns only in-flight migrations.
//
//	GET /api/v1/migrations/active
func (h *Handlers) ListActiveMigrations(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	list, err := h.Migrations.Store().ListActive(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*migration.Migration{}
	}
	writeJSON(w, http.StatusOK, list)
}

package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
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
	scopeReq := h.stackRes(r, name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksMigrate, scopeReq, "stack "+name)
		return
	}
	var req migration.MigrateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.TargetHostID == "" {
		writeError(w, http.StatusBadRequest, "target_host_id required")
		return
	}
	// The stack will run on the target, so moving it there needs deploy
	// rights on it there.
	if target := h.stackScopeReq(r.Context(), req.TargetHostID, name); !h.allowedOn(r, target, rbac.PermStacksDeploy) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, target, "stack "+name+" on the target host")
		return
	}
	userID := middleware.UserID(r.Context())
	m, err := h.Migrations.Initiate(r.Context(), name, req.TargetHostID, userID)
	if errors.Is(err, migration.ErrNamedVolumes) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":       "migrate",
		"migration_id": m.ID,
		"source":       m.SourceHostID,
		"target":       m.TargetHostID,
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
	m, ok := h.stackMigration(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// stackMigration loads migration {id} and requires it to belong to
// stack {name}, whose scope the route already checked.
func (h *Handlers) stackMigration(w http.ResponseWriter, r *http.Request) (*migration.Migration, bool) {
	m, err := h.Migrations.Store().Get(r.Context(), chi.URLParam(r, "id"))
	if err == nil && m.StackName != chi.URLParam(r, "name") {
		err = migration.ErrNotFound
	}
	if err != nil {
		if err == migration.ErrNotFound {
			writeError(w, http.StatusNotFound, "migration not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	return m, true
}

// visibleMigrations keeps the migrations of stacks the caller may view on
// the source or the target host.
func (h *Handlers) visibleMigrations(r *http.Request, list []*migration.Migration) []*migration.Migration {
	out := []*migration.Migration{}
	for _, m := range list {
		if middleware.AllowedOn(r.Context(), rbac.PermStacksView, h.stackScopeReq(r.Context(), m.SourceHostID, m.StackName)) ||
			middleware.AllowedOn(r.Context(), rbac.PermStacksView, h.stackScopeReq(r.Context(), m.TargetHostID, m.StackName)) {
			out = append(out, m)
		}
	}
	return out
}

// RollbackMigration triggers a manual rollback.
//
//	POST /api/v1/stacks/{name}/migrate/{id}/rollback
func (h *Handlers) RollbackMigration(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	scopeReq := h.stackRes(r, name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksMigrate, scopeReq, "stack "+name)
		return
	}
	if _, ok := h.stackMigration(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Migrations.Rollback(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	list = h.visibleMigrations(r, list)
	writeJSON(w, http.StatusOK, list)
}

// PreflightMigration runs pre-flight checks without starting a migration.
//
//	POST /api/v1/stacks/{name}/migrate/preflight { "target_host_id": "xxx" }
func (h *Handlers) PreflightMigration(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	scopeReq := h.stackRes(r, name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksMigrate, scopeReq, "stack "+name)
		return
	}
	var req migration.MigrateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	result, err := h.Migrations.Preflight(r.Context(), name, req.TargetHostID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// PurgeSource removes the source host's files/volumes after migration.
//
//	DELETE /api/v1/stacks/{name}/migrate/{id}/source
func (h *Handlers) PurgeSource(w http.ResponseWriter, r *http.Request) {
	if h.Migrations == nil {
		writeError(w, http.StatusServiceUnavailable, "migration service unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	scopeReq := h.stackRes(r, name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksMigrate, scopeReq, "stack "+name)
		return
	}
	if _, ok := h.stackMigration(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Migrations.PurgeSource(r.Context(), id); err != nil {
		if err == migration.ErrNotFound {
			writeError(w, http.StatusNotFound, "migration not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":       "migrate-purge-source",
		"migration_id": id,
	})
	w.WriteHeader(http.StatusNoContent)
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
	list = h.visibleMigrations(r, list)
	writeJSON(w, http.StatusOK, list)
}

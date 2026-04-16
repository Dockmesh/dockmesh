package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// ListRoles returns all roles (built-in + custom) with permissions.
//
//	GET /api/v1/roles
func (h *Handlers) ListRoles(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		// Fallback: return hardcoded roles.
		writeJSON(w, http.StatusOK, []rbac.CustomRole{
			{Name: "admin", Display: "Admin", Builtin: true, Permissions: rbac.RolePerms("admin")},
			{Name: "operator", Display: "Operator", Builtin: true, Permissions: rbac.RolePerms("operator")},
			{Name: "viewer", Display: "Viewer", Builtin: true, Permissions: rbac.RolePerms("viewer")},
		})
		return
	}
	writeJSON(w, http.StatusOK, h.Roles.List())
}

// GetRole returns a single role by name.
//
//	GET /api/v1/roles/{name}
func (h *Handlers) GetRole(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if h.Roles == nil {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	role, ok := h.Roles.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	writeJSON(w, http.StatusOK, role)
}

// CreateRole creates a new custom role.
//
//	POST /api/v1/roles
func (h *Handlers) CreateRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Name == "" || in.Display == "" {
		writeError(w, http.StatusBadRequest, "name and display required")
		return
	}
	if err := h.Roles.Create(r.Context(), in); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, in.Name, map[string]any{
		"action":      "role-create",
		"permissions": len(in.Permissions),
	})
	role, _ := h.Roles.Get(in.Name)
	writeJSON(w, http.StatusCreated, role)
}

// UpdateRole modifies a custom role (built-in roles cannot be edited).
//
//	PUT /api/v1/roles/{name}
func (h *Handlers) UpdateRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.Roles.Update(r.Context(), name, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, name, map[string]any{
		"action":      "role-update",
		"permissions": len(in.Permissions),
	})
	role, _ := h.Roles.Get(name)
	writeJSON(w, http.StatusOK, role)
}

// DeleteRole removes a custom role (built-in roles cannot be deleted).
//
//	DELETE /api/v1/roles/{name}
func (h *Handlers) DeleteRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	if err := h.Roles.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, name, map[string]any{"action": "role-delete"})
	w.WriteHeader(http.StatusNoContent)
}

// AllPermissions returns the full list of available permissions so the
// UI can render a permission picker for custom roles.
//
//	GET /api/v1/roles/permissions
func (h *Handlers) AllPermissions(w http.ResponseWriter, r *http.Request) {
	type permInfo struct {
		Name string `json:"name"`
		Desc string `json:"description"`
	}
	perms := []permInfo{
		{"read", "List/inspect everything non-sensitive"},
		{"container.control", "Start/stop/restart/remove containers"},
		{"container.exec", "Shell into containers"},
		{"stack.write", "Create/update/delete compose files"},
		{"stack.deploy", "Deploy/stop stacks + scale + migrate"},
		{"image.write", "Pull/remove/prune images"},
		{"image.scan", "Scan images for vulnerabilities"},
		{"network.write", "Create/remove networks"},
		{"volume.write", "Create/remove volumes"},
		{"user.manage", "Manage users, agents, backups, proxy, alerts"},
		{"audit.read", "View audit log"},
	}
	writeJSON(w, http.StatusOK, perms)
}

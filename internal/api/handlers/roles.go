package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// roleView is a role with how many assignments use it.
type roleView struct {
	rbac.Role
	Assignments int `json:"assignments"`
}

func (h *Handlers) rolesAvailable(w http.ResponseWriter) bool {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return false
	}
	return true
}

func (h *Handlers) ListRoles(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	counts := map[string]int{}
	for _, a := range h.Roles.AllAssignments() {
		counts[a.Role]++
	}
	roles := h.Roles.Roles()
	out := make([]roleView, 0, len(roles))
	for _, rd := range roles {
		out = append(out, roleView{Role: rd, Assignments: counts[rd.Name]})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetRole returns one role and its assignments.
//
//	GET /api/v1/roles/{name}
func (h *Handlers) GetRole(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	rd, ok := h.Roles.Role(chi.URLParam(r, "name"))
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	as := h.Roles.RoleAssignments(rd.Name)
	if as == nil {
		as = []rbac.Assignment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":        roleView{Role: rd, Assignments: len(as)},
		"assignments": h.withUsernames(r, as),
	})
}

// RoleCatalog returns the areas with their levels and the extras roles
// are built from.
//
//	GET /api/v1/roles/catalog
func (h *Handlers) RoleCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"areas": rbac.Areas, "extras": rbac.Extras})
}

func validRoleName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, c := range n {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (h *Handlers) CreateRole(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Display = strings.TrimSpace(in.Display)
	if !validRoleName(in.Name) || in.Display == "" {
		writeError(w, http.StatusBadRequest, "name (lower-case letters, digits, - and _) and display name required")
		return
	}
	if err := h.checkRoleInput(r, in); err != nil {
		writeGrantError(w, err)
		return
	}
	if err := h.Roles.CreateRole(r.Context(), in); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.audit(r, audit.ActionRoleCreate, in.Name, map[string]any{"levels": in.Levels, "extras": in.Extras})
	rd, _ := h.Roles.Role(in.Name)
	writeJSON(w, http.StatusCreated, roleView{Role: rd})
}

func (h *Handlers) UpdateRole(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cur, ok := h.requireManageRole(w, r, name)
	if !ok {
		return
	}
	in.Display = strings.TrimSpace(in.Display)
	if in.Display == "" {
		in.Display = cur.Display
	}
	if err := h.checkRoleInput(r, in); err != nil {
		writeGrantError(w, err)
		return
	}
	if err := h.checkRoleKeepsAdmin(name, rbac.IsAdminPermSet(rbac.PermsFor(in.Levels, in.Extras))); err != nil {
		writeLastAdminError(w, err)
		return
	}
	if err := h.Roles.UpdateRole(r.Context(), name, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionRoleUpdate, name, map[string]any{
		"before": map[string]any{"levels": cur.Levels, "extras": cur.Extras},
		"after":  map[string]any{"levels": in.Levels, "extras": in.Extras},
	})
	rd, _ := h.Roles.Role(name)
	writeJSON(w, http.StatusOK, roleView{Role: rd, Assignments: len(h.Roles.RoleAssignments(name))})
}

func (h *Handlers) DeleteRole(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	if _, ok := h.requireManageRole(w, r, name); !ok {
		return
	}
	if err := h.Roles.DeleteRole(r.Context(), name); err != nil {
		if errors.Is(err, rbac.ErrRoleInUse) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionRoleDelete, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

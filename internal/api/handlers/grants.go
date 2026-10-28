package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// errGrantExceedsCaller is returned when an assignment would reach beyond
// what the caller holds; handlers turn it into 403.
var errGrantExceedsCaller = errors.New("grant exceeds caller")

// checkGrant reports whether the caller may hand out role together with
// the user-level scope tags: every permission of the role must be one the
// caller effectively holds, and neither the role's scope nor the tags may
// reach further than the caller's own. The same check guards changing or
// removing an existing assignment, so nobody manages users or roles above
// themselves.
func (h *Handlers) checkGrant(r *http.Request, role string, scopeTags []string) error {
	perms, ok := h.permsForRoleName(role)
	if !ok {
		return fmt.Errorf("unknown role %q", role)
	}
	var scopes []rbac.Scope
	if h.Roles != nil {
		if rd, ok := h.Roles.Get(role); ok {
			scopes = rd.Scopes
		}
	}
	if err := h.checkPermsGrant(r, perms, scopes); err != nil {
		return err
	}
	return checkTagsGrant(r, scopeTags)
}

// checkTagsGrant requires user-level scope tags to stay within the
// caller's own tags when the caller has any.
func checkTagsGrant(r *http.Request, scopeTags []string) error {
	callerTags := middleware.ScopeTags(r.Context())
	if len(callerTags) == 0 {
		return nil
	}
	if len(scopeTags) == 0 {
		return fmt.Errorf("%w: you are limited to hosts tagged %v and can't grant access to all hosts", errGrantExceedsCaller, callerTags)
	}
	allowed := make(map[string]bool, len(callerTags))
	for _, t := range callerTags {
		allowed[t] = true
	}
	for _, t := range scopeTags {
		if !allowed[t] {
			return fmt.Errorf("%w: host tag %q is outside your scope", errGrantExceedsCaller, t)
		}
	}
	return nil
}

// checkPermsGrant is the role-definition half of checkGrant: perms must be
// held by the caller, and if the caller's roles are scoped, scopes must be
// non-empty and every entry must appear in each of those roles' scopes.
func (h *Handlers) checkPermsGrant(r *http.Request, perms map[rbac.Perm]bool, scopes []rbac.Scope) error {
	held := h.callerPermSet(r)
	for p := range perms {
		if !held[p] {
			return fmt.Errorf("%w: you don't hold %s", errGrantExceedsCaller, p)
		}
	}
	if !h.RBACv2Enforce || h.Roles == nil {
		return nil
	}
	for _, callerRole := range middleware.Roles(r.Context()) {
		rd, ok := h.Roles.Get(callerRole)
		if !ok || len(rd.Scopes) == 0 {
			continue
		}
		if len(scopes) == 0 {
			return fmt.Errorf("%w: your role is scoped and can't grant unscoped access", errGrantExceedsCaller)
		}
		own := make(map[rbac.Scope]bool, len(rd.Scopes))
		for _, sc := range rd.Scopes {
			own[sc] = true
		}
		for _, sc := range scopes {
			if !own[sc] {
				return fmt.Errorf("%w: scope %s=%s is outside your role's scope", errGrantExceedsCaller, sc.ScopeType, sc.ScopeValue)
			}
		}
	}
	return nil
}

// checkRoleInput validates a role definition against the caller's rights.
func (h *Handlers) checkRoleInput(r *http.Request, in rbac.RoleInput) error {
	perms := make(map[rbac.Perm]bool, len(in.Permissions))
	for _, p := range in.Permissions {
		perms[p] = true
	}
	return h.checkPermsGrant(r, perms, in.Scopes)
}

// requireManageRole checks that the caller holds everything the existing
// role grants before it is changed or deleted.
func (h *Handlers) requireManageRole(w http.ResponseWriter, r *http.Request, name string) bool {
	rd, ok := h.Roles.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return false
	}
	perms := make(map[rbac.Perm]bool, len(rd.Permissions))
	for _, p := range rd.Permissions {
		perms[p] = true
	}
	if err := h.checkPermsGrant(r, perms, rd.Scopes); err != nil {
		writeError(w, http.StatusForbidden, "you can't manage this role: "+err.Error())
		return false
	}
	return true
}

// permsGrantAdmin is isAdminRole for a permission list.
func permsGrantAdmin(perms []rbac.Perm) bool {
	have := make(map[rbac.Perm]bool, len(perms))
	for _, p := range perms {
		have[p] = true
	}
	for _, p := range adminPerms {
		if !have[p] {
			return false
		}
	}
	return true
}

// writeLastAdminError maps a checkKeepsAdmin error to a response.
func writeLastAdminError(w http.ResponseWriter, err error) {
	if errors.Is(err, errLastAdmin) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// writeGrantError maps a checkGrant error to a response.
func writeGrantError(w http.ResponseWriter, err error) {
	if errors.Is(err, errGrantExceedsCaller) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

var adminPerms = []rbac.Perm{rbac.PermSystemUpdate, rbac.PermUsersView, rbac.PermUsersCreate, rbac.PermUsersUpdate, rbac.PermUsersDelete}

// isAdminRole reports whether role can administer the install: it holds
// system.update and every users.* permission. The last such account must
// survive any change.
func (h *Handlers) isAdminRole(role string) bool {
	perms, ok := h.permsForRoleName(role)
	if !ok {
		return false
	}
	for _, p := range adminPerms {
		if !perms[p] {
			return false
		}
	}
	return true
}

var errLastAdmin = errors.New("this is the last account that can administer Dockmesh; give another account an admin role first")

// checkKeepsAdmin fails with errLastAdmin when userID currently holds an
// admin role, would lose it (newRole == "" means the account is removed),
// and no other account holds one.
func (h *Handlers) checkKeepsAdmin(ctx context.Context, userID, currentRole, newRole string) error {
	if !h.isAdminRole(currentRole) || (newRole != "" && h.isAdminRole(newRole)) {
		return nil
	}
	users, err := h.Auth.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.ID != userID && h.isAdminRole(u.Role) {
			return nil
		}
	}
	return errLastAdmin
}

// checkRoleKeepsAdmin is checkKeepsAdmin for editing or deleting a role:
// if role grants admin today and won't afterwards, some account outside
// it must still hold an admin role.
func (h *Handlers) checkRoleKeepsAdmin(ctx context.Context, role string, stillAdmin bool) error {
	if stillAdmin || !h.isAdminRole(role) {
		return nil
	}
	users, err := h.Auth.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Role != role && h.isAdminRole(u.Role) {
			return nil
		}
	}
	return errLastAdmin
}

// requireManageUser loads the target user and checks that the caller may
// manage that account as it is now. Writes the error and returns false
// otherwise.
func (h *Handlers) requireManageUser(w http.ResponseWriter, r *http.Request, id string) (role string, scopeTags []string, ok bool) {
	u, err := h.Auth.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return "", nil, false
	}
	// A user whose role was deleted holds nothing; only the tags matter.
	check := checkTagsGrant(r, u.ScopeTags)
	if _, known := h.permsForRoleName(u.Role); known {
		check = h.checkGrant(r, u.Role, u.ScopeTags)
	}
	if check != nil {
		writeError(w, http.StatusForbidden, "you can't manage this user: "+check.Error())
		return "", nil, false
	}
	return u.Role, u.ScopeTags, true
}

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// errGrantExceedsCaller is returned when an assignment would reach beyond
// what the caller holds; handlers turn it into 403.
var errGrantExceedsCaller = errors.New("grant exceeds caller")

var errLastAdmin = errors.New("this is the last account that can administer Dockmesh; give another account an admin role first")

// adminPerms make a role able to administer the install; the last account
// holding all of them must survive any change.
var adminPerms = []rbac.Perm{rbac.PermSystemUpdate, rbac.PermUsersView, rbac.PermUsersCreate, rbac.PermUsersUpdate, rbac.PermUsersDelete}

func permSet(ps []rbac.Perm) map[rbac.Perm]bool {
	out := make(map[rbac.Perm]bool, len(ps))
	for _, p := range ps {
		out[p] = true
	}
	return out
}

func hasAdminPerms(perms map[rbac.Perm]bool) bool {
	for _, p := range adminPerms {
		if !perms[p] {
			return false
		}
	}
	return true
}

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
	return h.checkPermsGrant(r, permSet(in.Permissions), in.Scopes)
}

// requireManageRole checks that the caller holds everything the existing
// role grants before it is changed or deleted.
func (h *Handlers) requireManageRole(w http.ResponseWriter, r *http.Request, name string) bool {
	rd, ok := h.Roles.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return false
	}
	if err := h.checkPermsGrant(r, permSet(rd.Permissions), rd.Scopes); err != nil {
		writeError(w, http.StatusForbidden, "you can't manage this role: "+err.Error())
		return false
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

// isAdminRole reports whether role holds every adminPerms permission.
func (h *Handlers) isAdminRole(role string) bool {
	perms, ok := h.permsForRoleName(role)
	return ok && hasAdminPerms(perms)
}

// otherAdminExists reports whether an account not matched by skip holds an
// admin role.
func (h *Handlers) otherAdminExists(ctx context.Context, skip func(u auth.User) bool) (bool, error) {
	users, err := h.Auth.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if !skip(u) && h.isAdminRole(u.Role) {
			return true, nil
		}
	}
	return false, nil
}

// checkKeepsAdmin fails with errLastAdmin when userID currently holds an
// admin role, would lose it (newRole == "" means the account is removed),
// and no other account holds one.
func (h *Handlers) checkKeepsAdmin(ctx context.Context, userID, currentRole, newRole string) error {
	if !h.isAdminRole(currentRole) || (newRole != "" && h.isAdminRole(newRole)) {
		return nil
	}
	ok, err := h.otherAdminExists(ctx, func(u auth.User) bool { return u.ID == userID })
	if err != nil || ok {
		return err
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
	ok, err := h.otherAdminExists(ctx, func(u auth.User) bool { return u.Role == role })
	if err != nil || ok {
		return err
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

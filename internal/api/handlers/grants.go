package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// errGrantExceedsCaller is returned when an assignment would reach beyond
// what the caller holds; handlers turn it into 403.
var errGrantExceedsCaller = errors.New("grant exceeds caller")

var errLastAdmin = errors.New("this is the last assignment of an admin role on everything; give another account the Admin role first")

// checkGrant reports whether the caller may hand out role on scope: the
// caller must hold every permission of the role everywhere the scope
// reaches. The same check guards changing or removing an assignment, so
// nobody manages access above their own.
func (h *Handlers) checkGrant(r *http.Request, role string, scope rbac.Scope) error {
	if h.Roles == nil {
		return errors.New("roles store unavailable")
	}
	perms, ok := h.Roles.RolePerms(role)
	if !ok {
		return fmt.Errorf("unknown role %q", role)
	}
	return h.checkPermsGrant(r, perms, scope)
}

func (h *Handlers) checkPermsGrant(r *http.Request, perms map[rbac.Perm]bool, scope rbac.Scope) error {
	acc := middleware.AccessOf(r.Context())
	for _, p := range rbac.SortedPerms(perms) {
		if rbac.IsBase(p) || (!scope.IsGlobal() && rbac.IsGlobalOnly(p)) {
			continue
		}
		if !acc.HoldsOn(p, scope, h.hostTags) {
			return fmt.Errorf("%w: you don't hold %s on %s", errGrantExceedsCaller, p, scope)
		}
	}
	return nil
}

// checkRoleInput validates a role definition against the caller's rights:
// a role can be assigned anywhere, so the caller needs its permissions on
// everything.
func (h *Handlers) checkRoleInput(r *http.Request, in rbac.RoleInput) error {
	if err := rbac.ValidateLevels(in.Levels, in.Extras); err != nil {
		return err
	}
	return h.checkPermsGrant(r, rbac.PermsFor(in.Levels, in.Extras), rbac.Global)
}

// requireManageRole checks that the caller holds everything the existing
// role grants, on everything, before it is changed or deleted.
func (h *Handlers) requireManageRole(w http.ResponseWriter, r *http.Request, name string) (rbac.Role, bool) {
	rd, ok := h.Roles.Role(name)
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return rbac.Role{}, false
	}
	if rd.Builtin {
		writeError(w, http.StatusConflict, rbac.ErrRoleBuiltin.Error())
		return rbac.Role{}, false
	}
	perms, _ := h.Roles.RolePerms(name)
	if err := h.checkPermsGrant(r, perms, rbac.Global); err != nil {
		writeError(w, http.StatusForbidden, "you can't manage this role: "+err.Error())
		return rbac.Role{}, false
	}
	return rd, true
}

// requireManageUser checks that the caller may manage every current
// assignment of the target user. Writes the error and returns false
// otherwise.
func (h *Handlers) requireManageUser(w http.ResponseWriter, r *http.Request, id string) ([]rbac.Assignment, bool) {
	if _, err := h.Auth.GetUser(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return nil, false
	}
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return nil, false
	}
	as := h.Roles.UserAssignments(id)
	for _, a := range as {
		if _, known := h.Roles.Role(a.Role); !known {
			continue
		}
		if err := h.checkGrant(r, a.Role, a.Scope); err != nil {
			writeError(w, http.StatusForbidden, "you can't manage this user: "+err.Error())
			return nil, false
		}
	}
	return as, true
}

// globalAdminAssignments counts the assignments of an admin role on
// everything, skipping those skip matches.
func (h *Handlers) globalAdminAssignments(skip func(a rbac.Assignment) bool) int {
	n := 0
	for _, a := range h.Roles.AllAssignments() {
		if h.Roles.IsGlobalAdmin(a) && (skip == nil || !skip(a)) {
			n++
		}
	}
	return n
}

// checkKeepsAdmin fails with errLastAdmin when removing the assignments
// skip matches would leave no admin role on everything.
func (h *Handlers) checkKeepsAdmin(skip func(a rbac.Assignment) bool) error {
	if h.Roles == nil {
		return nil
	}
	if h.globalAdminAssignments(nil) > 0 && h.globalAdminAssignments(skip) == 0 {
		return errLastAdmin
	}
	return nil
}

// checkRoleKeepsAdmin is checkKeepsAdmin for redefining or deleting a
// role: if it is an admin role today and won't be, some admin assignment
// of another role must remain.
func (h *Handlers) checkRoleKeepsAdmin(role string, stillAdmin bool) error {
	if stillAdmin || !h.Roles.IsAdminRole(role) {
		return nil
	}
	return h.checkKeepsAdmin(func(a rbac.Assignment) bool { return a.Role == role })
}

// SyncIdPAssignments replaces a user's identity provider assignments at
// sign-in. If that would remove the last admin assignment on everything,
// the old admin assignments stay and admins are told to fix the group
// mapping.
func (h *Handlers) SyncIdPAssignments(ctx context.Context, userID string, want []rbac.Assignment) error {
	if h.Roles == nil {
		return errors.New("roles store unavailable")
	}
	var known []rbac.Assignment
	for _, a := range want {
		if _, ok := h.Roles.Role(a.Role); ok && a.Scope.Validate() == nil {
			known = append(known, a)
		}
	}
	err := h.checkKeepsAdmin(func(a rbac.Assignment) bool { return a.UserID == userID && a.Source == rbac.SourceIdP })
	if errors.Is(err, errLastAdmin) {
		keepsAdmin := false
		for _, a := range known {
			keepsAdmin = keepsAdmin || h.Roles.IsGlobalAdmin(a)
		}
		if !keepsAdmin {
			for _, a := range h.Roles.UserAssignments(userID) {
				if a.Source == rbac.SourceIdP && h.Roles.IsGlobalAdmin(a) {
					known = append(known, a)
				}
			}
			h.raiseLastAdminNotice(ctx, userID)
		}
	}
	return h.Roles.ReplaceIdPAssignments(ctx, userID, known)
}

const lastAdminNoticeKey = "rbac-idp-last-admin"

func (h *Handlers) raiseLastAdminNotice(ctx context.Context, userID string) {
	if h.Notices == nil {
		return
	}
	name := userID
	if u, err := h.Auth.GetUser(ctx, userID); err == nil {
		name = u.Username
	}
	if err := h.Notices.Raise(ctx, notices.RaiseInput{
		Key: lastAdminNoticeKey, Audience: notices.Admins(), Severity: notices.SevWarning,
		Title: "Identity provider sign-in would have removed the last admin",
		Body: "The groups of " + name + " no longer map to the Admin role, but no other account holds Admin on everything. " +
			"Dockmesh kept the Admin assignment so the instance stays manageable. Give another account the Admin role or fix the group mapping, then remove it.",
		Link: "/users",
	}); err != nil {
		slog.Warn("raise last-admin notice", "err", err)
	}
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

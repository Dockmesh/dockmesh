package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// userView is a user with its assignments, as the users API returns it.
type userView struct {
	auth.User
	Assignments []rbac.Assignment `json:"assignments"`
}

func (h *Handlers) viewUser(u auth.User) userView {
	as := []rbac.Assignment{}
	if h.Roles != nil {
		as = append(as, h.Roles.UserAssignments(u.ID)...)
	}
	return userView{User: u, Assignments: as}
}

// withPermissions fills u.Permissions from access, for UI gating.
func withPermissions(u *auth.User, access *rbac.Access) {
	ps := access.Perms()
	u.Permissions = make([]string, 0, len(ps))
	for _, p := range ps {
		u.Permissions = append(u.Permissions, string(p))
	}
}

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
	// Role is assigned on Scope (default: everything); empty Role creates
	// an account without access.
	Role  string      `json:"role"`
	Scope *rbac.Scope `json:"scope,omitempty"`
}

type updateUserRequest struct {
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Source "sso" lets sign-in replace the identity provider
	// assignments; "manual" keeps access as it is. Empty = unchanged.
	Source string `json:"source,omitempty"`
}

type changePasswordRequest struct {
	// CurrentPassword is required when a user changes their OWN password.
	// Admins resetting another user's password may omit it (audited).
	CurrentPassword string `json:"current_password,omitempty"`
	Password        string `json:"password"`
}

func (h *Handlers) isValidRole(name string) bool {
	if h.Roles == nil {
		return false
	}
	_, ok := h.Roles.Role(name)
	return ok
}

func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, h.viewUser(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetUser returns one user with its assignments.
//
//	GET /api/v1/users/{id}
func (h *Handlers) GetUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.Auth.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, h.viewUser(*u))
}

func (h *Handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	scope := rbac.Global
	if req.Scope != nil && req.Scope.Type != "" {
		scope = *req.Scope
	}
	if req.Role != "" {
		if !h.isValidRole(req.Role) {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		if err := scope.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.checkGrant(r, req.Role, scope); err != nil {
			writeGrantError(w, err)
			return
		}
	}
	u, err := h.Auth.CreateUser(r.Context(), req.Username, req.Email, req.Password, "")
	if err != nil {
		status := http.StatusConflict
		msg := err.Error()
		switch {
		case errors.Is(err, auth.ErrUsernameTaken):
			msg = "username already in use"
		case errors.Is(err, auth.ErrEmailTaken):
			msg = "email already in use"
		case strings.Contains(msg, "password"):
			// password-policy errors from ValidatePassword are user-input
			// problems, not conflicts.
			status = http.StatusBadRequest
		}
		writeError(w, status, msg)
		return
	}
	h.audit(r, audit.ActionUserCreate, u.ID, map[string]string{"username": u.Username})
	if req.Role != "" {
		a, err := h.Roles.AddAssignment(r.Context(), rbac.Assignment{UserID: u.ID, Role: req.Role, Scope: scope, CreatedBy: actorID(r)})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.auditAssignment(r, audit.ActionAssignmentAdd, u.Username, a)
	}
	writeJSON(w, http.StatusCreated, h.viewUser(*u))
}

func (h *Handlers) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !h.requireManageUser(w, r, id) {
		return
	}
	switch req.Source {
	case "", auth.RoleSourceManual, auth.RoleSourceSSO:
	default:
		writeError(w, http.StatusBadRequest, "source must be manual or sso")
		return
	}
	u, err := h.Auth.UpdateUser(r.Context(), id, req.Email, req.DisplayName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	meta := map[string]any{}
	if req.Source != "" && req.Source != u.Source {
		if err := h.Auth.SetSource(r.Context(), id, req.Source); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		u.Source = req.Source
		meta["source"] = req.Source
	}
	h.audit(r, audit.ActionUserUpdate, id, meta)
	writeJSON(w, http.StatusOK, h.viewUser(*u))
}

func (h *Handlers) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == middleware.UserID(r.Context()) {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	if !h.requireManageUser(w, r, id) {
		return
	}
	if err := h.checkKeepsAdmin(func(a rbac.Assignment) bool { return a.UserID == id }); err != nil {
		writeLastAdminError(w, err)
		return
	}
	if err := h.Auth.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionUserDelete, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ChangeUserPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	callerID := middleware.UserID(r.Context())
	isSelf := id == callerID
	// Self, or a caller who manages users and every assignment of the
	// target. Resetting someone else's password needs no current password.
	if !isSelf && !middleware.Allowed(r.Context(), rbac.PermUsersUpdate) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !isSelf {
		if !h.requireManageUser(w, r, id) {
			return
		}
	}
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 chars")
		return
	}
	if isSelf {
		if req.CurrentPassword == "" {
			writeError(w, http.StatusBadRequest, "current_password required")
			return
		}
		ok, err := h.Auth.VerifyUserPassword(r.Context(), id, req.CurrentPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
	}
	if err := h.Auth.ChangePassword(r.Context(), id, req.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A changed password must lock out whoever else holds a session: on a
	// self-change every other session ends, on an admin reset all of them.
	keep := ""
	if isSelf {
		keep = middleware.SessionID(r.Context())
	}
	revoked, err := h.Auth.RevokeUserSessions(r.Context(), id, keep)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	meta := map[string]any{"sessions_revoked": revoked}
	if !isSelf {
		meta["admin_reset"] = true
		meta["target_user"] = id
	}
	h.audit(r, audit.ActionUserPassword, id, meta)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the currently authenticated caller — user OR api-token —
// with the permissions it holds anywhere, for UI gating. dmctl hits this
// as a post-login sanity check, so it must succeed for token callers.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	access := middleware.AccessOf(r.Context())
	uid := middleware.UserID(r.Context())
	if uid == "" {
		fake := &auth.User{Username: "api-token"}
		withPermissions(fake, access)
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":         "api_token",
			"username":     fake.Username,
			"api_token_id": middleware.APITokenID(r.Context()),
			"mfa_enabled":  false,
			"permissions":  fake.Permissions,
			"unscoped":     access.Unscoped(),
		})
		return
	}
	u, err := h.Auth.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	withPermissions(u, access)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "email": u.Email,
		"source": u.Source, "mfa_enabled": u.MFAEnabled, "last_login_at": u.LastLoginAt,
		"password_changed_at": u.PasswordChangedAt, "has_avatar": u.HasAvatar,
		"permissions": u.Permissions, "unscoped": access.Unscoped(),
		"assignments": h.viewUser(*u).Assignments,
	})
}

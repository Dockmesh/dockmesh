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

// enrichUserWithRBAC fills the User.Permissions + User.Scopes fields
// from the rbac.Store (or the in-code fallback for built-in roles).
// Idempotent — safe to call before writing /me or /login responses.
func enrichUserWithRBAC(roles *rbac.Store, u *auth.User) {
	if u == nil {
		return
	}
	scopes := &auth.MeScopes{Stacks: []string{}, Hosts: []string{}, HostTags: []string{}}
	if roles != nil {
		if r, ok := roles.Get(u.Role); ok {
			perms := make([]string, 0, len(r.Permissions))
			for _, p := range r.Permissions {
				perms = append(perms, string(p))
			}
			for _, sc := range r.Scopes {
				switch sc.ScopeType {
				case "stack":
					scopes.Stacks = append(scopes.Stacks, sc.ScopeValue)
				case "host":
					scopes.Hosts = append(scopes.Hosts, sc.ScopeValue)
				case "host_tag":
					scopes.HostTags = append(scopes.HostTags, sc.ScopeValue)
				}
			}
			u.Permissions = perms
			u.Scopes = scopes
			return
		}
	}
	// Fallback: role not in store cache (fresh install, pre-migration).
	// Mirror the in-code map for built-ins.
	perms := []string{}
	for _, p := range rbac.RolePerms(u.Role) {
		perms = append(perms, string(p))
	}
	u.Permissions = perms
	u.Scopes = scopes
}

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type updateUserRequest struct {
	Email       string `json:"email,omitempty"`
	Role        string `json:"role"` // empty = unchanged
	DisplayName string `json:"display_name,omitempty"`
	// ScopeTags: omitted = unchanged, [] = all hosts.
	ScopeTags *[]string `json:"scope_tags"`
}

type changePasswordRequest struct {
	// CurrentPassword is required when a user changes their OWN password.
	// Admins resetting another user's password may omit it (audited).
	CurrentPassword string `json:"current_password,omitempty"`
	Password        string `json:"password"`
}

func (h *Handlers) isValidRole(name string) bool {
	_, ok := h.permsForRoleName(name)
	return ok
}

func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
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
	if req.Role == "" {
		req.Role = "viewer"
	}
	if !h.isValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	// New accounts start without host scope, so a scoped caller can't
	// create one; invites carry scope tags for that.
	if err := h.checkGrant(r, req.Role, nil); err != nil {
		writeGrantError(w, err)
		return
	}
	u, err := h.Auth.CreateUser(r.Context(), req.Username, req.Email, req.Password, req.Role)
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
	h.audit(r, audit.ActionUserCreate, u.ID, map[string]string{"username": u.Username, "role": u.Role})
	writeJSON(w, http.StatusCreated, u)
}

func (h *Handlers) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	curRole, curTags, ok := h.requireManageUser(w, r, id)
	if !ok {
		return
	}
	role, tags := curRole, curTags
	if req.Role != "" {
		role = req.Role
	}
	if req.ScopeTags != nil {
		tags = *req.ScopeTags
	}
	if role != curRole && !h.isValidRole(role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	if err := h.checkGrant(r, role, tags); err != nil {
		writeGrantError(w, err)
		return
	}
	if err := h.checkKeepsAdmin(r.Context(), id, curRole, curTags, role, tags, false); err != nil {
		writeLastAdminError(w, err)
		return
	}
	u, err := h.Auth.UpdateUser(r.Context(), id, req.Email, role, req.DisplayName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.ScopeTags != nil {
		u, err = h.Auth.UpdateUserScope(r.Context(), id, tags)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.audit(r, audit.ActionUserUpdate, id, map[string]any{
		"role":       u.Role,
		"scope_tags": u.ScopeTags,
	})
	writeJSON(w, http.StatusOK, u)
}

func (h *Handlers) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == middleware.UserID(r.Context()) {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	role, tags, ok := h.requireManageUser(w, r, id)
	if !ok {
		return
	}
	if err := h.checkKeepsAdmin(r.Context(), id, role, tags, "", nil, true); err != nil {
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
	isAdmin := true
	for _, role := range middleware.Roles(r.Context()) {
		isAdmin = isAdmin && role == "admin"
	}
	// Self or admin can change password. Admin-for-other is allowed
	// without current password; admin-for-self still needs it.
	if !isSelf && !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !isSelf {
		if _, _, ok := h.requireManageUser(w, r, id); !ok {
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

// Me returns the currently authenticated caller — user OR api-token.
// dmctl hits this as a post-login sanity check, so it must succeed for
// token-authed requests even though there's no user record behind them.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		// API token path — no user session. Synthesise a User-shape
		// with the token's role so the frontend's permission gates
		// work the same whether the caller is a session or a token.
		tokRole := middleware.Role(r.Context())
		fake := &auth.User{
			Username: "api-token",
			Role:     tokRole,
		}
		enrichUserWithRBAC(h.Roles, fake)
		// An owned token only gets what its owner's role also allows.
		effective := make([]string, 0, len(fake.Permissions))
		for _, p := range fake.Permissions {
			if middleware.Allowed(r.Context(), rbac.Perm(p)) {
				effective = append(effective, p)
			}
		}
		fake.Permissions = effective
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":         "api_token",
			"username":     fake.Username,
			"role":         fake.Role,
			"api_token_id": middleware.APITokenID(r.Context()),
			"mfa_enabled":  false,
			"permissions":  fake.Permissions,
			"scopes":       fake.Scopes,
			"scope_tags":   middleware.ScopeTags(r.Context()),
		})
		return
	}
	u, err := h.Auth.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	enrichUserWithRBAC(h.Roles, u)
	writeJSON(w, http.StatusOK, u)
}

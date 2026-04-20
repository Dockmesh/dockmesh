package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/go-chi/chi/v5"
)

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type updateUserRequest struct {
	Email     string   `json:"email,omitempty"`
	Role      string   `json:"role"`
	ScopeTags []string `json:"scope_tags"` // empty = all hosts
}

type changePasswordRequest struct {
	// CurrentPassword is required when a user changes their OWN password.
	// Admins resetting another user's password may omit it (audited).
	CurrentPassword string `json:"current_password,omitempty"`
	Password        string `json:"password"`
}

// builtinRoles are always available regardless of what's in the rbac
// store. Custom roles created via POST /api/v1/roles are looked up
// dynamically at validation time.
var builtinRoles = map[string]bool{"admin": true, "operator": true, "viewer": true}

func (h *Handlers) isValidRole(name string) bool {
	if builtinRoles[name] {
		return true
	}
	if h.Roles == nil {
		return false
	}
	_, ok := h.Roles.Get(name)
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
	if req.Role != "" && !h.isValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	u, err := h.Auth.UpdateUser(r.Context(), id, req.Email, req.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Scope update lives in the same request body so the UI can edit
	// both in one save. Pass through nil vs [] distinction: nil means
	// the client didn't send the field (omitted in JSON), [] means
	// explicit clear. Simplest backend rule: always overwrite on
	// update — if a caller wants to preserve scope they send it back
	// unchanged.
	u, err = h.Auth.UpdateUserScope(r.Context(), id, req.ScopeTags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	isAdmin := middleware.Role(r.Context()) == "admin"
	// Self or admin can change password. Admin-for-other is allowed
	// without current password; admin-for-self still needs it.
	if !isSelf && !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
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
	meta := map[string]any{}
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
		// API token path — no user session. Return a token-shaped
		// identity so callers can confirm they're authenticated and
		// which role the token carries.
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":         "api_token",
			"username":     "api-token",
			"role":         middleware.Role(r.Context()),
			"api_token_id": middleware.APITokenID(r.Context()),
			"mfa_enabled":  false,
		})
		return
	}
	u, err := h.Auth.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

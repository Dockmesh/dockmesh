package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/go-chi/chi/v5"
)

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type updateUserRequest struct {
	Email string `json:"email,omitempty"`
	Role  string `json:"role"`
}

type changePasswordRequest struct {
	Password string `json:"password"`
}

var validRoles = map[string]bool{"admin": true, "operator": true, "viewer": true}

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
	if !validRoles[req.Role] {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	u, err := h.Auth.CreateUser(r.Context(), req.Username, req.Email, req.Password, req.Role)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
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
	if req.Role != "" && !validRoles[req.Role] {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	u, err := h.Auth.UpdateUser(r.Context(), id, req.Email, req.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionUserUpdate, id, map[string]string{"role": u.Role})
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
	// Self or admin can change password.
	if id != middleware.UserID(r.Context()) && middleware.Role(r.Context()) != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 chars")
		return
	}
	if err := h.Auth.ChangePassword(r.Context(), id, req.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionUserPassword, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the currently authenticated user.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	u, err := h.Auth.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

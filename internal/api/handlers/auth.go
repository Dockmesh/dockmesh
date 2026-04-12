package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	res, err := h.Auth.Login(r.Context(), req.Username, req.Password, r.UserAgent(), clientIP(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	res, err := h.Auth.Refresh(r.Context(), req.RefreshToken)
	switch {
	case errors.Is(err, auth.ErrTokenReused):
		writeError(w, http.StatusUnauthorized, "token reuse detected")
		return
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	_ = decodeJSON(r, &req)
	if req.RefreshToken != "" {
		_ = h.Auth.Logout(r.Context(), req.RefreshToken)
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	return r.RemoteAddr
}

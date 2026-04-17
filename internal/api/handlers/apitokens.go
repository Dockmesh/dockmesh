package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/go-chi/chi/v5"
)

// ListAPITokens returns all API tokens with metadata only — plaintext
// values are never returned after creation.
//
//	GET /api/v1/settings/api-tokens
func (h *Handlers) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	if h.APITokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens store unavailable")
		return
	}
	tokens, err := h.APITokens.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// CreateAPITokenInput is the POST body.
type CreateAPITokenInput struct {
	Name          string `json:"name"`
	Role          string `json:"role"`
	ExpiresInDays int    `json:"expires_in_days"` // 0 = never expires
}

// CreateAPIToken mints a new token and returns the plaintext ONCE.
// The response includes the full plaintext under `token`; subsequent
// reads only expose the prefix.
//
//	POST /api/v1/settings/api-tokens
func (h *Handlers) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.APITokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens store unavailable")
		return
	}

	var in CreateAPITokenInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	if in.Role == "" {
		writeError(w, http.StatusBadRequest, "role required")
		return
	}
	// Validate the requested role exists. Accept built-in names even if
	// Roles store is empty (pre-migration fallback).
	if h.Roles != nil {
		if _, ok := h.Roles.Get(in.Role); !ok {
			if _, ok := map[string]bool{"admin": true, "operator": true, "viewer": true}[in.Role]; !ok {
				writeError(w, http.StatusBadRequest, "unknown role")
				return
			}
		}
	}

	// Identify the creator from the JWT middleware.
	var creator *int64
	if uid := middleware.UserID(r.Context()); uid != "" {
		if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
			creator = &n
		}
	}

	plaintext, token, err := h.APITokens.Create(r.Context(), apitokens.CreateInput{
		Name:            in.Name,
		Role:            in.Role,
		ExpiresInDays:   in.ExpiresInDays,
		CreatedByUserID: creator,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.audit(r, "api_token.create", token.Prefix, map[string]any{
		"name":            token.Name,
		"role":            token.Role,
		"expires_in_days": in.ExpiresInDays,
	})

	// Return full plaintext this one time. UI must show it to the user
	// with a "Save this now, you won't see it again" warning.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         token.ID,
		"prefix":     token.Prefix,
		"name":       token.Name,
		"role":       token.Role,
		"expires_at": token.ExpiresAt,
		"token":      plaintext,
	})
}

// RevokeAPIToken revokes a token by ID.
//
//	DELETE /api/v1/settings/api-tokens/{id}
func (h *Handlers) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.APITokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens store unavailable")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// Load the token first so we can audit the name.
	existing, err := h.APITokens.Get(r.Context(), id)
	if errors.Is(err, apitokens.ErrNotFound) {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.APITokens.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit(r, "api_token.revoke", existing.Prefix, map[string]any{
		"name": existing.Name,
	})
	w.WriteHeader(http.StatusNoContent)
}

package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// callerCanManageOthers reports whether the JWT/token in r grants the
// tokens.manage_others permission. Drives the "see and revoke other
// users' tokens" gating split.
func (h *Handlers) callerCanManageOthers(r *http.Request) bool {
	if h.Roles == nil {
		// Pre-store fallback: built-in admins always have manage_others.
		return middleware.Role(r.Context()) == "admin"
	}
	role := middleware.Role(r.Context())
	if rd, ok := h.Roles.Get(role); ok {
		for _, p := range rd.Permissions {
			if p == rbac.PermTokensManageOthers {
				return true
			}
		}
		return false
	}
	return rbac.Allowed(role, rbac.PermTokensManageOthers)
}

// callerPermSet returns the effective v2.1 permission set for the
// authenticated caller — used for the privilege-escalation guard on
// CreateAPIToken. Returns nil if the role is unknown.
func (h *Handlers) callerPermSet(r *http.Request) map[rbac.Perm]bool {
	role := middleware.Role(r.Context())
	out := map[rbac.Perm]bool{}
	if h.Roles != nil {
		if rd, ok := h.Roles.Get(role); ok {
			for _, p := range rd.Permissions {
				out[p] = true
			}
			return out
		}
	}
	for _, p := range rbac.RolePerms(role) {
		out[p] = true
	}
	return out
}

// permsForRole returns the permission set for a role name — built-in or
// custom. Used by the privilege-escalation guard to check role-subset.
func (h *Handlers) permsForRoleName(name string) (map[rbac.Perm]bool, bool) {
	out := map[rbac.Perm]bool{}
	if h.Roles != nil {
		if rd, ok := h.Roles.Get(name); ok {
			for _, p := range rd.Permissions {
				out[p] = true
			}
			return out, true
		}
	}
	bi := rbac.RolePerms(name)
	if bi == nil {
		return nil, false
	}
	for _, p := range bi {
		out[p] = true
	}
	return out, true
}

// ListAPITokens returns API tokens. Callers without tokens.manage_others
// only see their own tokens; manage_others sees every row.
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
	if !h.callerCanManageOthers(r) {
		uid := middleware.UserID(r.Context())
		out := tokens[:0]
		for _, t := range tokens {
			if uid != "" && t.CreatedBy != nil && *t.CreatedBy == uid {
				out = append(out, t)
			}
		}
		tokens = out
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
	// Validate the requested role exists.
	requestedPerms, ok := h.permsForRoleName(in.Role)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown role")
		return
	}

	// Privilege-escalation guard (RBAC v2.1): caller may only mint a
	// token whose role grants a subset of the caller's own permissions.
	// Bypassed for callers with tokens.manage_others (typically admin).
	if !h.callerCanManageOthers(r) {
		callerPerms := h.callerPermSet(r)
		for p := range requestedPerms {
			if !callerPerms[p] {
				writeError(w, http.StatusForbidden,
					"cannot mint a token with permissions you don't have")
				return
			}
		}
	}

	// Identify the creator from the JWT middleware. users.id is a UUID
	// string in this codebase, so we pass it through as-is rather than
	// trying to coerce to int64 (the old code did, silently dropping
	// the value, leaving created_by NULL for every UI-issued token).
	var creator *string
	if uid := middleware.UserID(r.Context()); uid != "" {
		creator = &uid
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

	// Load the token first so we can audit the name + check ownership.
	existing, err := h.APITokens.Get(r.Context(), id)
	if errors.Is(err, apitokens.ErrNotFound) {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Ownership check: callers without manage_others can only revoke
	// their own tokens. 404 (not 403) hides the existence of other
	// users' tokens from probing.
	if !h.callerCanManageOthers(r) {
		uid := middleware.UserID(r.Context())
		if uid == "" || existing.CreatedBy == nil || *existing.CreatedBy != uid {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
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

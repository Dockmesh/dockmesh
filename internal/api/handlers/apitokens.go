package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// callerCanManageOthers reports whether the caller holds
// tokens.manage_others. Drives the "see and revoke other users' tokens"
// gating split.
func (h *Handlers) callerCanManageOthers(r *http.Request) bool {
	return middleware.Allowed(r.Context(), rbac.PermTokensManageOthers)
}

// callerPermSet returns the caller's effective permissions: for an owned
// API token, only what both the token's role and its owner's role grant.
func (h *Handlers) callerPermSet(r *http.Request) map[rbac.Perm]bool {
	out := map[rbac.Perm]bool{}
	for i, role := range middleware.Roles(r.Context()) {
		perms, _ := h.permsForRoleName(role)
		if i == 0 {
			for p := range perms {
				out[p] = true
			}
			continue
		}
		for p := range out {
			if !perms[p] {
				delete(out, p)
			}
		}
	}
	return out
}

// callerTokenOwner is the user that owns tokens the caller creates or may
// manage as their own: the signed-in user, or the owner of the API token
// in use. Empty for legacy tokens.
func callerTokenOwner(r *http.Request) string {
	if uid := middleware.UserID(r.Context()); uid != "" {
		return uid
	}
	return middleware.TokenOwnerID(r.Context())
}

// syncLegacyTokenNotice re-evaluates the legacy-token admin notice after
// the token set changed.
func (h *Handlers) syncLegacyTokenNotice(r *http.Request) {
	if h.APITokens == nil {
		return
	}
	if err := h.APITokens.SyncLegacyNotice(r.Context(), h.Notices); err != nil {
		slog.Warn("legacy api token notice", "err", err)
	}
}

// permsForRoleName returns the permission set for a role name — built-in
// or custom.
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
	// Expiry happens without a request; listing is where admins look.
	h.syncLegacyTokenNotice(r)
	if !h.callerCanManageOthers(r) {
		uid := callerTokenOwner(r)
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

	// The new token's role may not grant more than the caller effectively
	// holds. No exception for tokens.manage_others: a token holding it
	// would otherwise mint one with its owner's full role.
	callerPerms := h.callerPermSet(r)
	for p := range requestedPerms {
		if !callerPerms[p] {
			writeError(w, http.StatusForbidden,
				"cannot mint a token with permissions you don't have")
			return
		}
	}

	// A token minted with another token belongs to that token's owner.
	// Legacy tokens have no owner to hand down.
	creator := callerTokenOwner(r)
	if creator == "" {
		writeError(w, http.StatusForbidden,
			"legacy API tokens can't create tokens; sign in, or use a token created after the upgrade")
		return
	}

	// A token minted by a token can't outlive it, or revoking a leaked
	// token wouldn't end the access it handed out.
	var notAfter *time.Time
	if parentID := middleware.APITokenID(r.Context()); parentID != 0 {
		parent, err := h.APITokens.Get(r.Context(), parentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		notAfter = parent.ExpiresAt
	}

	plaintext, token, err := h.APITokens.Create(r.Context(), apitokens.CreateInput{
		Name:            in.Name,
		Role:            in.Role,
		ExpiresInDays:   in.ExpiresInDays,
		NotAfter:        notAfter,
		CreatedByUserID: creator,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.syncLegacyTokenNotice(r)
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
		"created_by": token.CreatedBy,
		"legacy":     false,
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
	// their own tokens, and an API token only itself, so a weak token
	// can't take down its owner's other tokens. 404 (not 403) hides the
	// existence of other tokens from probing.
	if !h.callerCanManageOthers(r) {
		uid := callerTokenOwner(r)
		self := middleware.APITokenID(r.Context())
		if uid == "" || existing.CreatedBy == nil || *existing.CreatedBy != uid || (self != 0 && self != id) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
	}

	if err := h.APITokens.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.syncLegacyTokenNotice(r)
	h.audit(r, "api_token.revoke", existing.Prefix, map[string]any{
		"name": existing.Name,
	})
	w.WriteHeader(http.StatusNoContent)
}

package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// callerCanManageOthers reports whether the caller holds
// tokens.manage_others. Drives the "see and revoke other users' tokens"
// gating split.
func (h *Handlers) callerCanManageOthers(r *http.Request) bool {
	return middleware.Allowed(r.Context(), rbac.PermTokensManageOthers)
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

// CreateAPITokenInput is the POST body. Role and Scope optionally
// restrict the token below its owner's access; an empty Role means the
// token acts with the owner's full current access.
type CreateAPITokenInput struct {
	Name          string      `json:"name"`
	Role          string      `json:"role"`
	Scope         *rbac.Scope `json:"scope,omitempty"`
	ExpiresInDays int         `json:"expires_in_days"` // 0 = never expires
}

// CreateAPIToken mints a new token and returns the plaintext ONCE.
// The response includes the full plaintext under `token`; subsequent
// reads only expose the prefix.
//
//	POST /api/v1/settings/api-tokens
func (h *Handlers) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.APITokens == nil || h.Roles == nil {
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
	scope := rbac.Global
	if in.Scope != nil && in.Scope.Type != "" {
		scope = *in.Scope
	}
	if err := scope.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Role == "" && !scope.IsGlobal() {
		writeError(w, http.StatusBadRequest, "a scope restriction needs a role")
		return
	}
	if in.Role != "" {
		if _, ok := h.Roles.Role(in.Role); !ok {
			writeError(w, http.StatusBadRequest, "unknown role")
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
	// A token minted by a restricted token stays inside that restriction.
	var notAfter *time.Time
	if parentID := middleware.APITokenID(r.Context()); parentID != 0 {
		parent, err := h.APITokens.Get(r.Context(), parentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		notAfter = parent.ExpiresAt
		if parent.Role != "" {
			if in.Role == "" {
				in.Role, scope = parent.Role, parent.Scope
			}
			if !h.restrictionWithin(parent, in.Role, scope) {
				writeError(w, http.StatusForbidden, "a token can only create tokens restricted at least as far as itself")
				return
			}
		}
	}

	plaintext, token, err := h.APITokens.Create(r.Context(), apitokens.CreateInput{
		Name:            in.Name,
		Role:            in.Role,
		Scope:           scope,
		ExpiresInDays:   in.ExpiresInDays,
		NotAfter:        notAfter,
		CreatedByUserID: creator,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.syncLegacyTokenNotice(r)
	h.audit(r, audit.ActionAPITokenCreate, token.Prefix, map[string]any{
		"name":            token.Name,
		"role":            token.Role,
		"scope":           token.Scope.String(),
		"expires_in_days": in.ExpiresInDays,
	})

	// Return full plaintext this one time. UI must show it to the user
	// with a "Save this now, you won't see it again" warning.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         token.ID,
		"prefix":     token.Prefix,
		"name":       token.Name,
		"role":       token.Role,
		"scope":      token.Scope,
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
	h.audit(r, audit.ActionAPITokenRevoke, existing.Prefix, map[string]any{
		"name": existing.Name,
	})
	w.WriteHeader(http.StatusNoContent)
}

// restrictionWithin reports whether role on scope grants nothing beyond
// parent's restriction.
func (h *Handlers) restrictionWithin(parent *apitokens.Token, role string, scope rbac.Scope) bool {
	pp, ok := h.Roles.RolePerms(parent.Role)
	if !ok {
		return false
	}
	np, ok := h.Roles.RolePerms(role)
	if !ok {
		return false
	}
	for p := range np {
		if !pp[p] {
			return false
		}
	}
	ps := parent.Scope
	if ps.Type == "" {
		ps = rbac.Global
	}
	return rbac.ScopeContains(ps, scope, h.hostTags)
}

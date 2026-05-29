package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/notifications"
)

// CreateInviteInput is the admin-side request: pick a role + scope and
// an optional email hint so the accept page can confirm "you're the
// right person for this link".
type CreateInviteInput struct {
	Role      string   `json:"role"`
	ScopeTags []string `json:"scope_tags"`
	EmailHint string   `json:"email_hint"`
	// TTLHours overrides the default 24h expiry. Range clamped to
	// 1..168 (1 hour … 7 days) so a stray "0" doesn't mint a
	// never-expiring invite.
	TTLHours int `json:"ttl_hours"`
}

// CreateInvite mints a one-time invite link.
//
//	POST /api/v1/invites
func (h *Handlers) CreateInvite(w http.ResponseWriter, r *http.Request) {
	if h.Invites == nil {
		writeError(w, http.StatusServiceUnavailable, "invites not configured")
		return
	}
	var in CreateInviteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Role = strings.TrimSpace(in.Role)
	if in.Role == "" {
		writeError(w, http.StatusBadRequest, "role required")
		return
	}
	ttl := time.Duration(in.TTLHours) * time.Hour
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	invite, err := h.Invites.Create(r.Context(), invites.CreateInput{
		Role:      in.Role,
		ScopeTags: in.ScopeTags,
		EmailHint: in.EmailHint,
		TTL:       ttl,
		CreatedBy: middleware.UserID(r.Context()),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.invite_created", in.EmailHint, map[string]any{
		"role":       in.Role,
		"scope_tags": in.ScopeTags,
		"invite_id":  invite.ID,
	})
	writeJSON(w, http.StatusCreated, invite)
}

// ListInvites returns recent invites (admin overview).
//
//	GET /api/v1/invites?limit=100
func (h *Handlers) ListInvites(w http.ResponseWriter, r *http.Request) {
	if h.Invites == nil {
		writeJSON(w, http.StatusOK, []invites.Invite{})
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
		}
	}
	list, err := h.Invites.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// RevokeInvite deletes a pending invite by id.
//
//	DELETE /api/v1/invites/{id}
func (h *Handlers) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	if h.Invites == nil {
		writeError(w, http.StatusServiceUnavailable, "invites not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Invites.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.invite_revoked", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// PreviewInvite is the public read used by the accept page to render
// "Invitation as <role> for <email_hint>". Does NOT reveal who created
// the invite — keep it minimal.
//
//	GET /api/v1/invite/{token}
//
// Public — no auth required.
func (h *Handlers) PreviewInvite(w http.ResponseWriter, r *http.Request) {
	if h.Invites == nil {
		writeError(w, http.StatusServiceUnavailable, "invites not configured")
		return
	}
	token := chi.URLParam(r, "token")
	inv, err := h.Invites.Get(r.Context(), token)
	switch {
	case errors.Is(err, invites.ErrNotFound):
		writeError(w, http.StatusNotFound, "invite not found")
		return
	case errors.Is(err, invites.ErrExpired):
		writeError(w, http.StatusGone, "invite expired")
		return
	case errors.Is(err, invites.ErrUsed):
		writeError(w, http.StatusGone, "invite already used")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Strip private fields before returning to the public.
	writeJSON(w, http.StatusOK, map[string]any{
		"role":       inv.Role,
		"scope_tags": inv.ScopeTags,
		"email_hint": inv.EmailHint,
		"expires_at": inv.ExpiresAt,
	})
}

// AcceptInviteInput is the recipient's side: pick a username + password
// and walk away with a real account.
type AcceptInviteInput struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AcceptInvite consumes the token + creates the user. Returns the
// usual access + refresh token pair so the recipient is signed in
// immediately.
//
//	POST /api/v1/invite/{token}/accept
//
// Public — no auth required. Rate-limited at the router layer.
func (h *Handlers) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if h.Invites == nil || h.Auth == nil {
		writeError(w, http.StatusServiceUnavailable, "invites not configured")
		return
	}
	token := chi.URLParam(r, "token")
	inv, err := h.Invites.Get(r.Context(), token)
	switch {
	case errors.Is(err, invites.ErrNotFound):
		writeError(w, http.StatusNotFound, "invite not found")
		return
	case errors.Is(err, invites.ErrExpired):
		writeError(w, http.StatusGone, "invite expired")
		return
	case errors.Is(err, invites.ErrUsed):
		writeError(w, http.StatusGone, "invite already used")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var in AcceptInviteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	in.Email = strings.TrimSpace(in.Email)
	if in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	user, err := h.Auth.CreateUser(r.Context(), in.Username, in.Email, in.Password, inv.Role)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUsernameTaken):
			writeError(w, http.StatusConflict, "username taken")
		case errors.Is(err, auth.ErrEmailTaken):
			writeError(w, http.StatusConflict, "email taken")
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	// Apply scope tags if the invite carries any.
	if len(inv.ScopeTags) > 0 {
		if _, err := h.Auth.UpdateUserScope(r.Context(), user.ID, inv.ScopeTags); err != nil {
			// User exists but scope failed to apply. Surface the
			// error rather than silently leaving them unscoped.
			writeError(w, http.StatusInternalServerError, "user created but scope failed: "+err.Error())
			return
		}
	}

	// Burn the token after the user is fully provisioned. Race-loss
	// here is OK: the token was bound to one role, two simultaneous
	// accepts would conflict on the username UNIQUE constraint above.
	_ = h.Invites.MarkUsed(r.Context(), token)

	h.audit(r, audit.ActionUserCreate, user.Username, map[string]any{
		"via":       "invite",
		"role":      inv.Role,
		"scope_tags": inv.ScopeTags,
	})

	// Bell-icon ping to the admin who minted the invite. Per-user
	// (UserID set) — broadcast would spam every other admin too.
	if h.Notifications != nil && inv.CreatedBy != "" {
		_, _ = h.Notifications.Emit(r.Context(), notifications.EmitInput{
			UserID:   inv.CreatedBy,
			Kind:     notifications.KindInviteAccepted,
			Severity: notifications.SevSuccess,
			Title:    "Invite accepted: " + user.Username,
			Body:     "Your invitation was redeemed — they now have a " + inv.Role + " account",
			Link:     "/users",
		})
	}

	// Sign the new user in immediately so they land in the dashboard.
	// StartSessionForSSO mints a fresh token pair without password /
	// MFA — same shape we use after a verified SSO callback.
	result, err := h.Auth.StartSessionForSSO(r.Context(), *user, r.UserAgent(), inviteClientIP(r))
	if err != nil {
		// User exists but session minting failed — they can log in
		// manually. Return 201 + user so the UI can redirect to /login.
		writeJSON(w, http.StatusCreated, map[string]any{"user": user})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":          result.User,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
	})
}

// inviteClientIP extracts the caller's IP for the invite session.
// Mirrors middleware.clientIP — duplicated here to avoid cross-package
// dependency on an internal helper.
func inviteClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

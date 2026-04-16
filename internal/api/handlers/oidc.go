package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const oidcStateCookie = "dockmesh_oidc"

// oidcStateClaims wraps a Pending payload in a signed JWT so we can
// round-trip it via an httpOnly cookie.
type oidcStateClaims struct {
	Pending *oidc.Pending `json:"p"`
	jwt.RegisteredClaims
}

// -----------------------------------------------------------------------------
// Public (unauthenticated) endpoints
// -----------------------------------------------------------------------------

// ListOIDCProvidersPublic is called by the login page to render SSO buttons.
func (h *Handlers) ListOIDCProvidersPublic(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeJSON(w, http.StatusOK, []oidc.PublicProvider{})
		return
	}
	list, err := h.OIDC.ListEnabledPublic(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// OIDCLogin redirects the browser to the provider's authorization URL.
func (h *Handlers) OIDCLogin(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}
	slug := chi.URLParam(r, "slug")
	url, pending, err := h.OIDC.StartLogin(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Persist the pending state as a signed short-lived cookie.
	claims := oidcStateClaims{
		Pending: pending,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dockmesh",
			Subject:   "oidc-pending",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.JWTSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	http.Redirect(w, r, url, http.StatusFound)
}

// OIDCCallback handles the provider redirect, completes the exchange,
// provisions the user, and redirects to the SPA with tokens in the
// URL fragment.
func (h *Handlers) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}

	// Handle provider-side error (user clicked "Deny" etc.).
	if errStr := r.URL.Query().Get("error"); errStr != "" {
		http.Redirect(w, r, "/login?sso_error="+errStr, http.StatusFound)
		return
	}

	c, err := r.Cookie(oidcStateCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing state cookie")
		return
	}
	// Clear the cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})

	parsed, err := jwt.ParseWithClaims(c.Value, &oidcStateClaims{}, func(t *jwt.Token) (any, error) {
		return h.JWTSecret, nil
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid state cookie: "+err.Error())
		return
	}
	claims, ok := parsed.Claims.(*oidcStateClaims)
	if !ok || !parsed.Valid || claims.Pending == nil {
		writeError(w, http.StatusUnauthorized, "invalid state cookie")
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}

	res, err := h.OIDC.HandleCallback(r.Context(), claims.Pending, code, state, r.UserAgent(), clientIP(r))
	if errors.Is(err, oidc.ErrInvalidState) {
		writeError(w, http.StatusUnauthorized, "state mismatch")
		return
	}
	if err != nil {
		// Log the full error for debugging but show a friendly message.
		http.Redirect(w, r, "/login?sso_error="+encodeQuery(err.Error()), http.StatusFound)
		return
	}
	if res.User != nil {
		h.audit(r, "auth.sso_login", res.User.ID, map[string]string{"provider": claims.Pending.Slug})
	}

	// Hand tokens to the SPA via URL fragment (fragment is never sent to
	// the server by the browser, so it doesn't leak to logs).
	loc := "/login#sso_access=" + res.AccessToken + "&sso_refresh=" + res.RefreshToken
	http.Redirect(w, r, loc, http.StatusFound)
}

// -----------------------------------------------------------------------------
// Admin endpoints
// -----------------------------------------------------------------------------

func (h *Handlers) ListOIDCProviders(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeJSON(w, http.StatusOK, []oidc.Provider{})
		return
	}
	list, err := h.OIDC.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) CreateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}
	var in oidc.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.OIDC.CreateProvider(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "oidc.provider_create", p.Slug, nil)
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handlers) UpdateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in oidc.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.OIDC.UpdateProvider(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "oidc.provider_update", idStr, nil)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) DeleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.OIDC.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "oidc.provider_delete", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ReloadOIDCProviders flushes the OIDC provider cache so the next
// login re-discovers every issuer from scratch. Useful when IdP
// config changes without touching the Dockmesh DB rows.
func (h *Handlers) ReloadOIDCProviders(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc not configured")
		return
	}
	h.OIDC.ReloadAll()
	h.audit(r, "oidc.reload", "", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func encodeQuery(s string) string {
	// Minimal encoder so we don't need net/url dep just for this helper.
	return (&queryEscaper{s: s}).escape()
}

type queryEscaper struct{ s string }

func (q *queryEscaper) escape() string {
	out := make([]byte, 0, len(q.s))
	for i := 0; i < len(q.s); i++ {
		c := q.s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			out = append(out, c)
		} else {
			out = append(out, '%', hexChar(c>>4), hexChar(c&0xf))
		}
	}
	return string(out)
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + (b - 10)
}

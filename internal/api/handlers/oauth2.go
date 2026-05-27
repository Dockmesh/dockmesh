package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/oauth2auth"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const oauth2StateCookie = "dockmesh_oauth2"

type oauth2StateClaims struct {
	Pending *oauth2auth.Pending `json:"p"`
	jwt.RegisteredClaims
}

// -----------------------------------------------------------------------------
// Public
// -----------------------------------------------------------------------------

func (h *Handlers) ListOAuth2ProvidersPublic(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeJSON(w, http.StatusOK, []oauth2auth.PublicProvider{})
		return
	}
	list, err := h.OAuth2.ListEnabledPublic(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) OAuth2Login(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	slug := chi.URLParam(r, "slug")
	url, pending, err := h.OAuth2.StartLogin(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	claims := oauth2StateClaims{
		Pending: pending,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dockmesh",
			Subject:   "oauth2-pending",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.JWTSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauth2StateCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *Handlers) OAuth2Callback(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	if errStr := r.URL.Query().Get("error"); errStr != "" {
		http.Redirect(w, r, "/login?sso_error="+errStr, http.StatusFound)
		return
	}
	c, err := r.Cookie(oauth2StateCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing state cookie")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauth2StateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	parsed, err := jwt.ParseWithClaims(c.Value, &oauth2StateClaims{}, func(t *jwt.Token) (any, error) {
		return h.JWTSecret, nil
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid state cookie: "+err.Error())
		return
	}
	claims, ok := parsed.Claims.(*oauth2StateClaims)
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
	res, err := h.OAuth2.HandleCallback(r.Context(), claims.Pending, code, state, r.UserAgent(), clientIP(r))
	if errors.Is(err, oauth2auth.ErrInvalidState) {
		writeError(w, http.StatusUnauthorized, "state mismatch")
		return
	}
	if err != nil {
		http.Redirect(w, r, "/login?sso_error="+encodeQuery(err.Error()), http.StatusFound)
		return
	}
	if res.User != nil {
		h.audit(r, "auth.sso_login", res.User.ID, map[string]string{"provider": claims.Pending.Slug, "kind": "oauth2"})
	}
	loc := "/login#sso_access=" + res.AccessToken + "&sso_refresh=" + res.RefreshToken
	http.Redirect(w, r, loc, http.StatusFound)
}

// -----------------------------------------------------------------------------
// Admin
// -----------------------------------------------------------------------------

func (h *Handlers) ListOAuth2Providers(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeJSON(w, http.StatusOK, []oauth2auth.Provider{})
		return
	}
	list, err := h.OAuth2.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) GetOAuth2Provider(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	p, err := h.OAuth2.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) CreateOAuth2Provider(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	var in oauth2auth.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.OAuth2.CreateProvider(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "oauth2.provider_create", p.Slug, nil)
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handlers) UpdateOAuth2Provider(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in oauth2auth.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.OAuth2.UpdateProvider(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "oauth2.provider_update", idStr, nil)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) DeleteOAuth2Provider(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.OAuth2.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "oauth2.provider_delete", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) TestOAuth2Provider(w http.ResponseWriter, r *http.Request) {
	if h.OAuth2 == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth2 not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	prov, err := h.OAuth2.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if terr := h.OAuth2.TestConfig(r.Context(), prov); terr != nil {
		_ = h.OAuth2.RecordProviderTest(r.Context(), id, false, terr.Error())
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": terr.Error(),
		})
		return
	}
	_ = h.OAuth2.RecordProviderTest(r.Context(), id, true, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

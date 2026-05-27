package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/ldapauth"
	"github.com/go-chi/chi/v5"
)

// -----------------------------------------------------------------------------
// Public
// -----------------------------------------------------------------------------

func (h *Handlers) ListLDAPProvidersPublic(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeJSON(w, http.StatusOK, []ldapauth.PublicProvider{})
		return
	}
	list, err := h.LDAP.ListEnabledPublic(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type ldapLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LDAPLogin verifies a username+password pair against the named LDAP
// provider and, on success, mints a Dockmesh session pair the SPA can
// use exactly like a local login.
func (h *Handlers) LDAPLogin(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	slug := chi.URLParam(r, "slug")
	var in ldapLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := h.LDAP.Authenticate(r.Context(), slug, in.Username, in.Password, r.UserAgent(), clientIP(r))
	if errors.Is(err, ldapauth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if errors.Is(err, ldapauth.ErrProviderNotFound) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.User != nil {
		h.audit(r, "auth.sso_login", res.User.ID, map[string]string{"provider": slug, "kind": "ldap"})
	}
	writeJSON(w, http.StatusOK, res)
}

// -----------------------------------------------------------------------------
// Admin
// -----------------------------------------------------------------------------

func (h *Handlers) ListLDAPProviders(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeJSON(w, http.StatusOK, []ldapauth.Provider{})
		return
	}
	list, err := h.LDAP.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) GetLDAPProvider(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	p, err := h.LDAP.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) CreateLDAPProvider(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	var in ldapauth.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.LDAP.CreateProvider(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "ldap.provider_create", p.Slug, nil)
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handlers) UpdateLDAPProvider(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in ldapauth.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.LDAP.UpdateProvider(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "ldap.provider_update", idStr, nil)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) DeleteLDAPProvider(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.LDAP.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "ldap.provider_delete", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestLDAPProvider opens a connection + service bind to verify the
// admin's config before users hit it. Failure surfaces as
// {ok:false, error:"…"}.
func (h *Handlers) TestLDAPProvider(w http.ResponseWriter, r *http.Request) {
	if h.LDAP == nil {
		writeError(w, http.StatusServiceUnavailable, "ldap not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if terr := h.LDAP.TestConnection(r.Context(), id); terr != nil {
		_ = h.LDAP.RecordProviderTest(r.Context(), id, false, terr.Error())
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": terr.Error(),
		})
		return
	}
	_ = h.LDAP.RecordProviderTest(r.Context(), id, true, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

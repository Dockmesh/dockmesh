package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/saml"
	"github.com/go-chi/chi/v5"
)

const samlStateCookie = "dockmesh_saml"

type samlStateClaims struct {
	Pending *saml.Pending `json:"p"`
	auth.TypedClaims
}

// -----------------------------------------------------------------------------
// Public (unauthenticated) endpoints — drive the SSO login round-trip
// -----------------------------------------------------------------------------

// ListSAMLProvidersPublic is read by the login page to render SAML SSO
// buttons alongside OIDC ones.
func (h *Handlers) ListSAMLProvidersPublic(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeJSON(w, http.StatusOK, []saml.PublicProvider{})
		return
	}
	list, err := h.SAML.ListEnabledPublic(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// SAMLLogin builds an AuthnRequest, signs a Pending cookie, and
// redirects the browser to the IdP's SSO endpoint.
func (h *Handlers) SAMLLogin(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	slug := chi.URLParam(r, "slug")
	relay := saml.RandomRelayState()
	url, pending, err := h.SAML.StartLogin(r.Context(), slug, relay)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	token, err := h.Auth.TokenKeys().Sign(auth.KindSAMLState, 5*time.Minute, &samlStateClaims{Pending: pending})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.setSAMLStateCookie(w, r, token, 300)
	http.Redirect(w, r, url, http.StatusFound)
}

// samlNeedsHTTPSMsg explains the usual reason the state cookie is missing.
const samlNeedsHTTPSMsg = "SAML sign-in did not return to the browser session it started from. " +
	"With an identity provider on another domain, Dockmesh must be reached over HTTPS."

// SAMLACS handles the assertion-consumer POST from the IdP. Validates
// the response, finds/creates the user, issues Dockmesh session tokens,
// and lands the user back in the SPA with tokens in the URL fragment.
func (h *Handlers) SAMLACS(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	c, err := r.Cookie(samlStateCookie)
	if err != nil {
		msg := "SAML sign-in session expired or missing; sign in again."
		if !clientip.Secure(r) {
			msg = samlNeedsHTTPSMsg
		}
		ssoErrorRedirect(w, r, msg)
		return
	}
	// Clear the cookie immediately — single-use.
	h.setSAMLStateCookie(w, r, "", -1)
	claims := &samlStateClaims{}
	if err := h.Auth.TokenKeys().Parse(auth.KindSAMLState, c.Value, claims); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid state cookie")
		return
	}
	if claims.Pending == nil {
		writeError(w, http.StatusUnauthorized, "invalid state cookie")
		return
	}

	res, err := h.SAML.HandleACS(r.Context(), claims.Pending, r, r.UserAgent(), clientIP(r))
	if err != nil {
		http.Redirect(w, r, "/login?sso_error="+encodeQuery(err.Error()), http.StatusFound)
		return
	}
	if res.RekeyedUser != "" {
		h.audit(r, audit.ActionSAMLRekey, res.RekeyedUser, map[string]string{"provider": claims.Pending.Slug})
		h.syncSAMLRekeyNotice(r.Context())
	}
	if res.Login.User != nil {
		h.audit(r, "auth.sso_login", res.Login.User.ID, map[string]string{"provider": claims.Pending.Slug, "kind": "saml"})
	}
	h.finishSSOLogin(w, r, res.Login)
}

// SAMLMetadata serves the SP-side metadata XML for the given slug.
// IdP admins paste this URL into their IdP to register Dockmesh.
func (h *Handlers) SAMLMetadata(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	slug := chi.URLParam(r, "slug")
	body, err := h.SAML.SPMetadata(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(body)
}

// -----------------------------------------------------------------------------
// Admin endpoints — provider CRUD + metadata test
// -----------------------------------------------------------------------------

func (h *Handlers) ListSAMLProviders(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeJSON(w, http.StatusOK, []saml.Provider{})
		return
	}
	list, err := h.SAML.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) GetSAMLProvider(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	p, err := h.SAML.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) CreateSAMLProvider(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	var in saml.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.SAML.CreateProvider(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "saml.provider_create", p.Slug, nil)
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handlers) UpdateSAMLProvider(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in saml.ProviderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.SAML.UpdateProvider(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "saml.provider_update", idStr, nil)
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) DeleteSAMLProvider(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.SAML.DeleteProvider(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "saml.provider_delete", idStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestSAMLProvider re-parses a configured provider's stored metadata
// and persists the outcome on the row. The frontend renders a
// green/red dot from last_test_ok and surfaces last_test_error in a
// tooltip.
// GetSAMLRekey lists the accounts still waiting for their one-time re-key.
func (h *Handlers) GetSAMLRekey(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	p, err := h.SAML.PendingRekeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// EndSAMLRekey closes the one-time re-key for every account that is still
// waiting for it.
func (h *Handlers) EndSAMLRekey(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	n, err := h.SAML.EndRekey(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionSAMLRekeyEnd, "", map[string]string{"accounts": strconv.FormatInt(n, 10)})
	h.syncSAMLRekeyNotice(r.Context())
	writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
}

// syncSAMLRekeyNotice updates the pending re-key notice. A failure only
// delays it until the next change or restart.
func (h *Handlers) syncSAMLRekeyNotice(ctx context.Context) {
	if h.SAML == nil || h.Notices == nil {
		return
	}
	if err := h.SAML.SyncRekeyNotice(ctx, h.Notices); err != nil {
		slog.Warn("saml re-key notice update failed", "err", err)
	}
}

func (h *Handlers) TestSAMLProvider(w http.ResponseWriter, r *http.Request) {
	if h.SAML == nil {
		writeError(w, http.StatusServiceUnavailable, "saml not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	prov, err := h.SAML.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_ = ctx
	report, terr := h.SAML.TestMetadata(prov.IdPMetadataXML)
	if terr != nil {
		_ = h.SAML.RecordProviderTest(r.Context(), id, false, terr.Error())
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": terr.Error(),
		})
		return
	}
	_ = h.SAML.RecordProviderTest(r.Context(), id, true, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"entity_id": report.EntityID,
		"sso_url":   report.SSOURL,
		"slo_url":   report.SLOURL,
		"has_cert":  report.HasCert,
	})
}

package handlers

import (
	"net/http"
)

// UnifiedProvider is the wire shape returned by
// GET /api/v1/auth/providers. Four backend tables (oidc/oauth2/saml/
// ldap) project into one row each so the frontend can render one
// "Providers" list without four separate fetches.
//
// `raw` carries the full provider record so screens that need
// kind-specific fields (e.g. the SAML metadata URL or the LDAP bind
// DN) don't have to fall back to the per-kind endpoint.
type UnifiedProvider struct {
	Kind        string `json:"kind"` // oidc | oauth2 | saml | ldap
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Raw         any    `json:"raw,omitempty"`
}

// ListAuthProviders returns OIDC + OAuth2 + SAML + LDAP providers in
// one list with a `kind` discriminator. Each underlying service
// degrades gracefully — a service that's not wired (nil) contributes
// an empty slice rather than failing the whole call.
//
//	GET /api/v1/auth/providers
func (h *Handlers) ListAuthProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []UnifiedProvider{}

	if h.OIDC != nil {
		if list, err := h.OIDC.ListProviders(ctx); err == nil {
			for _, p := range list {
				out = append(out, UnifiedProvider{
					Kind:        "oidc",
					ID:          p.ID,
					Slug:        p.Slug,
					DisplayName: p.DisplayName,
					Enabled:     p.Enabled,
					Raw:         p,
				})
			}
		}
	}
	if h.OAuth2 != nil {
		if list, err := h.OAuth2.ListProviders(ctx); err == nil {
			for _, p := range list {
				out = append(out, UnifiedProvider{
					Kind:        "oauth2",
					ID:          p.ID,
					Slug:        p.Slug,
					DisplayName: p.DisplayName,
					Enabled:     p.Enabled,
					Raw:         p,
				})
			}
		}
	}
	if h.SAML != nil {
		if list, err := h.SAML.ListProviders(ctx); err == nil {
			for _, p := range list {
				out = append(out, UnifiedProvider{
					Kind:        "saml",
					ID:          p.ID,
					Slug:        p.Slug,
					DisplayName: p.DisplayName,
					Enabled:     p.Enabled,
					Raw:         p,
				})
			}
		}
	}
	if h.LDAP != nil {
		if list, err := h.LDAP.ListProviders(ctx); err == nil {
			for _, p := range list {
				out = append(out, UnifiedProvider{
					Kind:        "ldap",
					ID:          p.ID,
					Slug:        p.Slug,
					DisplayName: p.DisplayName,
					Enabled:     p.Enabled,
					Raw:         p,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

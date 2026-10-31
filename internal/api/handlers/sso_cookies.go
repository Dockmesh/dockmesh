package handlers

import (
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// cookiesSecure reports whether cookies should carry the Secure flag: the
// request arrived over TLS (directly or via a trusted proxy), or the
// configured base URL is https so the browser only ever talks TLS to us.
func (h *Handlers) cookiesSecure(r *http.Request) bool {
	if clientip.Secure(r) {
		return true
	}
	if h.Settings == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Settings.Get(settings.KeyBaseURL, ""))), "https://")
}

// setStateCookie sets (maxAge > 0) or clears (maxAge < 0) an SSO state
// cookie.
func (h *Handlers) setStateCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	h.writeStateCookie(w, r, name, value, maxAge, http.SameSiteLaxMode)
}

// setSAMLStateCookie is setStateCookie for SAML, whose IdP answers with a
// cross-site POST: a Lax cookie isn't sent with it, so over HTTPS it is
// SameSite=None. Browsers accept that only with Secure, so over plain
// HTTP it stays Lax and works only with an IdP on the same site.
func (h *Handlers) setSAMLStateCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	mode := http.SameSiteLaxMode
	if h.cookiesSecure(r) {
		mode = http.SameSiteNoneMode
	}
	h.writeStateCookie(w, r, samlStateCookie, value, maxAge, mode)
}

func (h *Handlers) writeStateCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int, mode http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookiesSecure(r),
		SameSite: mode,
		MaxAge:   maxAge,
	})
}

// ssoErrorRedirect sends the browser to the sign-in page with an error an
// identity provider passed back. The value is attacker-controllable (any
// link to the callback), so it is shortened and fully escaped.
func ssoErrorRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	if runes := []rune(msg); len(runes) > 200 {
		msg = string(runes[:200])
	}
	http.Redirect(w, r, "/login?sso_error="+encodeQuery(msg), http.StatusFound)
}

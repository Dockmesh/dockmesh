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
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookiesSecure(r),
		SameSite: http.SameSiteLaxMode,
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

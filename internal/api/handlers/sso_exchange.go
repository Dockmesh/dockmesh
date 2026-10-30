package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
)

const (
	ssoCodeCookie = "dockmesh_sso_code"
	ssoCodeTTL    = 60 * time.Second
	ssoCodeMax    = 1024
)

type ssoCodeEntry struct {
	login   *auth.LoginResult
	expires time.Time
}

// ssoCodes holds finished SSO logins for a minute until the SPA that
// started them trades the one-time code for tokens. Codes live in memory
// only: a restart in that window just means signing in again.
type ssoCodes struct {
	mu      sync.Mutex
	entries map[string]ssoCodeEntry
}

func (s *ssoCodes) put(login *auth.LoginResult) (string, bool) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]ssoCodeEntry{}
	}
	for k, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, k)
		}
	}
	if len(s.entries) >= ssoCodeMax {
		return "", false
	}
	s.entries[code] = ssoCodeEntry{login: login, expires: now.Add(ssoCodeTTL)}
	return code, true
}

// take removes and returns the login for code; a code works once.
func (s *ssoCodes) take(code string) *auth.LoginResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[code]
	if !ok {
		return nil
	}
	delete(s.entries, code)
	if time.Now().After(e.expires) {
		return nil
	}
	return e.login
}

// finishSSOLogin ends an SSO callback. The tokens stay on the server; the
// browser gets a one-time code, also set as an httpOnly cookie so only the
// browser that completed the identity-provider round trip can redeem it.
// A code planted in someone else's link therefore signs nobody in.
func (h *Handlers) finishSSOLogin(w http.ResponseWriter, r *http.Request, login *auth.LoginResult) {
	code, ok := h.ssoCodes.put(login)
	if !ok {
		ssoErrorRedirect(w, r, "too many pending sign-ins, try again")
		return
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the request scheme via cookiesSecure; HttpOnly and SameSite=Strict are set
		Name:     ssoCodeCookie,
		Value:    code,
		Path:     "/api/v1/auth/sso/exchange",
		HttpOnly: true,
		Secure:   h.cookiesSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ssoCodeTTL / time.Second),
	})
	http.Redirect(w, r, "/login?sso_code="+code, http.StatusFound)
}

type ssoExchangeRequest struct {
	Code string `json:"code"`
}

// SSOExchange trades the one-time code from an SSO callback for the
// session tokens.
func (h *Handlers) SSOExchange(w http.ResponseWriter, r *http.Request) {
	limitBody(w, r)
	var req ssoExchangeRequest
	if err := decodeJSON(r, &req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "code required")
		return
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the request scheme via cookiesSecure; HttpOnly and SameSite=Strict are set
		Name:     ssoCodeCookie,
		Path:     "/api/v1/auth/sso/exchange",
		HttpOnly: true,
		Secure:   h.cookiesSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	c, err := r.Cookie(ssoCodeCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(req.Code)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	login := h.ssoCodes.take(req.Code)
	if login == nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	writeJSON(w, http.StatusOK, login)
}

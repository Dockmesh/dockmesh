package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	limitBody(w, r)
	ip := clientIP(r)
	key := limitKey(ip)

	// Per-IP limit, generous so users behind one proxy don't block each
	// other; the per-account limit in auth.Service is the main guard.
	if h.LoginLimiter != nil {
		if ok, retry := h.LoginLimiter.Check(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "too many login attempts — try again later")
			return
		}
	}

	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	res, err := h.Auth.Login(r.Context(), req.Username, req.Password, r.UserAgent(), ip)
	var rateErr *auth.RateLimitError
	if errors.As(err, &rateErr) {
		writeRetryAfter(w, rateErr.Until)
		writeError(w, http.StatusTooManyRequests, "too many login attempts — try again later")
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if h.LoginLimiter != nil {
			if until, started := h.LoginLimiter.Fail(key); started {
				h.auditBlock(r, "ip", key, "login", auth.BlockRateLimit, until)
			}
		}
		h.auditAccountBlocks(r, err, "login")
		if h.Audit != nil {
			h.Audit.Write(r.Context(), "", audit.ActionLoginFailed, req.Username, map[string]string{"ip": ip})
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var lockErr *auth.LockoutError
	if errors.As(err, &lockErr) {
		if h.Audit != nil {
			h.Audit.Write(r.Context(), "", audit.ActionLoginFailed, req.Username,
				map[string]string{"ip": ip, "reason": "locked"})
		}
		writeLocked(w, lockErr.Until)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	// Don't audit MFA-pending state — audit happens after /auth/mfa succeeds.
	if h.Audit != nil && res.User != nil {
		h.Audit.Write(r.Context(), res.User.ID, audit.ActionLogin, req.Username, map[string]string{"ip": ip})
	}
	// Enrich the response with v2.1 permissions + scopes so the frontend
	// doesn't have to round-trip /me after login to gate UI.
	enrichUserWithRBAC(h.Roles, res.User)
	writeJSON(w, http.StatusOK, res)
}

// Per-IP login limit.
const (
	loginIPMax    = 100
	loginIPWindow = 15 * time.Minute
)

// NewLoginLimiter returns the per-IP limiter for password logins: loginIPMax
// failures within loginIPWindow block the address for as long.
func NewLoginLimiter() *ratelimit.Limiter {
	return ratelimit.New(loginIPMax, loginIPWindow, loginIPWindow)
}

// writeLocked answers 423 with the actual unlock time: locks expire on
// their own, so the user can just wait. Retry-After is the standard
// header for 423.
func writeLocked(w http.ResponseWriter, until time.Time) {
	wait := time.Until(until)
	if wait < 0 {
		wait = 0
	}
	secs := int(wait.Round(time.Second).Seconds())
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	var msg string
	if secs < 60 {
		msg = fmt.Sprintf("account temporarily locked — try again in %d seconds", secs)
	} else {
		mins := (secs + 59) / 60 // round up so "try in 1 minute" never becomes "0 minutes"
		msg = fmt.Sprintf("account temporarily locked — try again in %d minute%s", mins, plural(mins))
	}
	writeError(w, http.StatusLocked, msg)
}

func writeRetryAfter(w http.ResponseWriter, until time.Time) {
	w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(until).Seconds())+1))
}

// auditBlock records the start of a sign-in block, once per block.
func (h *Handlers) auditBlock(r *http.Request, kind, key, endpoint, reason string, until time.Time) {
	if h.Audit == nil {
		return
	}
	h.Audit.Write(r.Context(), "", audit.ActionLoginBlocked, key, map[string]string{
		"kind":     kind,
		"key":      key,
		"endpoint": endpoint,
		"reason":   reason,
		"until":    until.UTC().Format(time.RFC3339),
	})
}

// auditAccountBlocks records the account blocks a failed attempt started.
func (h *Handlers) auditAccountBlocks(r *http.Request, err error, endpoint string) {
	var attempt *auth.AttemptError
	if !errors.As(err, &attempt) {
		return
	}
	for _, b := range attempt.Blocks {
		h.auditBlock(r, "account", b.Key, endpoint, b.Reason, b.Until)
	}
}

// limitKey normalises the IP so that different TCP source ports collapse
// to the same bucket.
func limitKey(ip string) string {
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return clientip.LimitKey(ip)
}

func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	limitBody(w, r)
	var req refreshRequest
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	// Only per IP and loose: refresh tokens can't be guessed, the limit just
	// keeps a client from hammering the endpoint. A token that fails
	// verification names no account we could trust.
	ip := clientIP(r)
	if !h.refreshLimits.allow(w, ip, "") {
		return
	}
	res, err := h.Auth.Refresh(r.Context(), req.RefreshToken)
	if errors.Is(err, auth.ErrInvalidToken) {
		h.refreshLimits.fail(ip, "")
	}
	switch {
	case errors.Is(err, auth.ErrTokenReused):
		writeError(w, http.StatusUnauthorized, "token reuse detected")
		return
	case errors.Is(err, auth.ErrSessionOutdated):
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "signed out after a security update, please sign in again",
			"code":  "session_outdated",
		})
		return
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	_ = decodeJSON(r, &req)
	if req.RefreshToken != "" {
		_ = h.Auth.Logout(r.Context(), req.RefreshToken)
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string { return clientip.IP(r) }

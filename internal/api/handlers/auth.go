package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
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
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if len(req.Username) > 256 {
		writeError(w, http.StatusBadRequest, "username too long")
		return
	}
	ip := clientIP(r)
	key := limitKey(ip)
	// Per-IP limit, generous so users behind one proxy don't block each
	// other; the per-account limit in auth.Service is the main guard.
	ipTicket, ok := acquireIP(w, h.LoginLimiter, key, "too many login attempts")
	if !ok {
		return
	}
	res, err := h.Auth.Login(r.Context(), req.Username, req.Password, r.UserAgent(), ip)
	h.settleIP(r, h.LoginLimiter, ipTicket, key, "login", errors.Is(err, auth.ErrInvalidCredentials))
	// The attempt is counted; record its outcome even if the client left.
	ctx := context.WithoutCancel(r.Context())
	var rateErr *auth.RateLimitError
	if errors.As(err, &rateErr) {
		writeTooMany(w, "too many login attempts", time.Until(rateErr.Until))
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		h.auditAccountBlocks(r, err, "login")
		if h.Audit != nil {
			h.Audit.Write(ctx, "", audit.ActionLoginFailed, req.Username, map[string]string{"ip": ip})
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	var lockErr *auth.LockoutError
	if errors.As(err, &lockErr) {
		if h.Audit != nil {
			h.Audit.Write(ctx, "", audit.ActionLoginFailed, req.Username,
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
		h.Audit.Write(ctx, res.User.ID, audit.ActionLogin, req.Username, map[string]string{"ip": ip})
	}
	// Permissions let the frontend gate the UI without a /me round trip.
	if res.User != nil {
		withPermissions(res.User, middleware.UserAccess(res.User.ID))
	}
	writeJSON(w, http.StatusOK, res)
}

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
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second).Seconds())))
	writeError(w, http.StatusLocked, "account temporarily locked — try again in "+waitText(wait))
}

func writeRetryAfter(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
}

// writeTooMany answers 429 with the wait in the header and the message.
func writeTooMany(w http.ResponseWriter, prefix string, wait time.Duration) {
	writeRetryAfter(w, wait)
	writeError(w, http.StatusTooManyRequests, prefix+" — try again in "+waitText(wait))
}

func waitText(wait time.Duration) string {
	secs := int(wait.Round(time.Second).Seconds())
	if secs < 60 {
		return fmt.Sprintf("%d second%s", secs, plural(secs))
	}
	mins := (secs + 59) / 60 // round up so "try in 1 minute" never becomes "0 minutes"
	return fmt.Sprintf("%d minute%s", mins, plural(mins))
}

// acquireIP counts a sign-in attempt from key against l, answering 429
// while the address is blocked. A nil limiter allows everything.
func acquireIP(w http.ResponseWriter, l *ratelimit.Limiter, key, prefix string) (ratelimit.Ticket, bool) {
	if l == nil {
		return ratelimit.Ticket{}, true
	}
	t, ok, retry := l.Acquire(key)
	if !ok {
		writeTooMany(w, prefix, retry)
	}
	return t, ok
}

// settleIP finishes an attempt acquired with acquireIP: a failure may start
// a block, anything else is taken back so only failures count.
func (h *Handlers) settleIP(r *http.Request, l *ratelimit.Limiter, t ratelimit.Ticket, key, endpoint string, failed bool) {
	if l == nil {
		return
	}
	if !failed {
		l.Refund(t)
		return
	}
	if until, started := l.Settle(t); started {
		h.auditBlock(r, "ip", key, endpoint, auth.BlockRateLimit, until)
	}
}

// auditBlock records the start of a sign-in block, once per block.
func (h *Handlers) auditBlock(r *http.Request, kind, key, endpoint, reason string, until time.Time) {
	if h.Audit == nil {
		return
	}
	h.Audit.Write(context.WithoutCancel(r.Context()), "", audit.ActionLoginBlocked, key, map[string]string{
		"kind":     kind,
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
	att, ok := h.refreshLimits.acquire(w, ip, "")
	if !ok {
		return
	}
	res, err := h.Auth.Refresh(r.Context(), req.RefreshToken)
	h.refreshLimits.finish(att, errors.Is(err, auth.ErrInvalidToken))
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

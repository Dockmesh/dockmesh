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
	ip := clientIP(r)
	key := limitKey(ip)

	// Brute-force guard (§1.5): 10 failures per minute → 5 min lockout.
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
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if h.LoginLimiter != nil {
			h.LoginLimiter.Fail(key)
		}
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
		// Tell the user the actual unlock time so they can just wait.
		// The previous "contact an administrator" copy was misleading —
		// locks auto-expire via time.Now() >= locked_until, no admin
		// action needed. Retry-After is the standard header for 423.
		wait := time.Until(lockErr.Until)
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
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if h.LoginLimiter != nil {
		h.LoginLimiter.Succeed(key)
	}
	// Don't audit MFA-pending state — audit happens after /auth/mfa succeeds.
	if h.Audit != nil && res.User != nil {
		h.Audit.Write(r.Context(), res.User.ID, audit.ActionLogin, req.Username, map[string]string{"ip": ip})
	}
	writeJSON(w, http.StatusOK, res)
}

// limitKey normalises the IP so that different TCP source ports collapse
// to the same bucket.
func limitKey(ip string) string {
	host, _, err := net.SplitHostPort(ip)
	if err == nil {
		return host
	}
	return ip
}

func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	res, err := h.Auth.Refresh(r.Context(), req.RefreshToken)
	switch {
	case errors.Is(err, auth.ErrTokenReused):
		writeError(w, http.StatusUnauthorized, "token reuse detected")
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

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	return r.RemoteAddr
}

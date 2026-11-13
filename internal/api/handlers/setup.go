package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/google/uuid"
)

// SetupStatus returns the wizard's "are we still in setup mode" answer.
// Called by the wizard shell on every page load so it can redirect the
// operator to the dashboard once setup is complete.
func (h *Handlers) SetupStatus(w http.ResponseWriter, r *http.Request) {
	if h.SetupState == nil {
		writeJSON(w, http.StatusOK, setup.Status{Active: false})
		return
	}
	writeJSON(w, http.StatusOK, h.SetupState.SnapshotStatus(middleware.SetupToken(r)))
}

// SetupCode checks a setup code. A right one opens the setup session,
// which takes over from any earlier one, and sets its cookie.
func (h *Handlers) SetupCode(w http.ResponseWriter, r *http.Request) {
	if h.SetupState == nil || !h.SetupState.Active() {
		writeError(w, http.StatusGone, "setup mode is no longer active")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	token, err := h.SetupState.Codes().Redeem(clientip.IP(r), req.Code)
	var re *setup.RedeemError
	switch {
	case errors.As(err, &re):
		writeSetupCodeError(w, re)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SetupCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func writeSetupCodeError(w http.ResponseWriter, re *setup.RedeemError) {
	body := map[string]any{"code_state": re.State}
	status := http.StatusUnauthorized
	switch {
	case re.RateLimited:
		status = http.StatusTooManyRequests
		secs := int(re.RetryAfter.Round(time.Second).Seconds())
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		body["error"] = "too many wrong setup codes from this address"
		body["retry_after_secs"] = secs
	case re.State == setup.CodeNone:
		status = http.StatusConflict
		body["error"] = "no setup code has been created"
	case re.State == setup.CodeExpired:
		status = http.StatusGone
		body["error"] = "the setup code has expired"
	case re.State == setup.CodeLocked:
		status = http.StatusLocked
		body["error"] = "the setup code is locked after too many wrong attempts"
	default:
		body["error"] = "wrong setup code"
		body["attempts_left"] = re.AttemptsLeft
	}
	writeJSON(w, status, body)
}

// SetupPreflight runs the host-fact + Docker-health checks the
// wizard's first page renders, for a setup session.
func (h *Handlers) SetupPreflight(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	pf := setup.CollectPreflight(r.Context(), h.Docker)
	writeJSON(w, http.StatusOK, pf)
}

// SetupServerInfo feeds the wizard's top-bar (version · os · ip · docker).
// The wizard polls this every ~10s so uptime stays live while the
// operator is configuring. Lightweight on purpose.
func (h *Handlers) SetupServerInfo(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	startedAt := time.Now()
	if h.SetupState != nil {
		startedAt = h.SetupState.StartedAt()
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	si := setup.CollectServerInfo(r.Context(), wizardServerVersion(), h.Docker, host, startedAt)
	writeJSON(w, http.StatusOK, si)
}

// SetupValidateDataDir is the live-validate Step 2 endpoint. Body:
// `{ "path": "/data" }`. Returns the DataDirCheck struct verbatim.
func (h *Handlers) SetupValidateDataDir(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	writeJSON(w, http.StatusOK, setup.CheckDataDir(req.Path))
}

// SetupValidateUser checks an existing-or-new system user choice.
// Body: `{ "mode": "existing" | "create", "username": "dockmesh" }`.
func (h *Handlers) SetupValidateUser(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	var req struct {
		Mode     string `json:"mode"`
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch req.Mode {
	case "existing":
		writeJSON(w, http.StatusOK, setup.CheckSystemUser(req.Username))
	case "create":
		writeJSON(w, http.StatusOK, setup.CheckNewUser(req.Username))
	default:
		writeError(w, http.StatusBadRequest, "mode must be 'existing' or 'create'")
	}
}

// SetupTestURL is Step 5's "Test connection" button — issues a GET
// against the supplied URL with a 5s timeout and reports back.
func (h *Handlers) SetupTestURL(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	var req struct {
		URL          string `json:"url"`
		ExpectHealth bool   `json:"expect_health"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	writeJSON(w, http.StatusOK, setup.CheckURL(r.Context(), req.URL, req.ExpectHealth, localPort(r)))
}

// SetupCommit kicks off the install. Returns 200 with a run_id; the
// real progress comes via /setup/stream/{run_id}. Decoupled so the
// frontend can subscribe to the stream BEFORE the runner finishes —
// the alternative (commit returns when done) would block the HTTP
// connection for several seconds and lose intermediate events.
func (h *Handlers) SetupCommit(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	var in setup.CommitInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.SetupCommit_ == nil {
		writeError(w, http.StatusServiceUnavailable, "install runner not wired")
		return
	}
	if !h.SetupState.TryBeginCommit(middleware.SetupToken(r)) {
		writeError(w, http.StatusConflict, "an install is already running, or setup has finished or expired")
		return
	}
	// An account may have been created since boot, e.g. with the CLI.
	if !h.setupStillFree(w, r) {
		return
	}

	runner := setup.NewRunner()
	runID := uuid.NewString()
	h.setupRuns.Store(runID, runner)

	go func() {
		// Detach from the request context so the install isn't
		// cancelled when the operator's commit POST returns. The
		// runner uses its own background context.
		ctx := context.Background()
		runner.Run(ctx, in, h.SetupCommit_)
		done, err := runner.Done()
		// An account created meanwhile ends setup just like a finished install.
		h.SetupState.EndCommit(done && (err == nil || errors.Is(err, auth.ErrAlreadySetUp)))
		// Keep the runner around for a while so late subscribers can
		// still read the event history. After 10 minutes drop it.
		go func() {
			time.Sleep(10 * time.Minute)
			h.setupRuns.Delete(runID)
		}()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":     runID,
		"stream_url": "/api/v1/setup/stream/" + runID,
	})
}

// localPort is the port of the listener that accepted r, 0 if unknown.
func localPort(r *http.Request) uint16 {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0
	}
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return 0
	}
	return ap.Port()
}

// setupWritable answers 410 once setup is over and 401 without a setup
// session that may still write. SetupGate refuses the same requests; this
// keeps each handler safe on its own.
func (h *Handlers) setupWritable(w http.ResponseWriter, r *http.Request) bool {
	if h.SetupState == nil || !h.SetupState.Active() {
		writeError(w, http.StatusGone, "setup mode is no longer active")
		return false
	}
	if h.SetupState.Codes().Session(middleware.SetupToken(r)) != setup.SessionWrite {
		writeError(w, http.StatusUnauthorized, "setup code required")
		return false
	}
	return true
}

// SetupStream is the Server-Sent-Events endpoint Step 7's terminal
// reads from. Each install step from the runner becomes one SSE
// `event: line` frame. Closes the connection when the runner finishes.
func (h *Handlers) SetupStream(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimPrefix(r.URL.Path, "/api/v1/setup/stream/")
	v, ok := h.setupRuns.Load(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "run not found or already evicted")
		return
	}
	runner := v.(*setup.Runner)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported on this transport")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if proxied
	w.WriteHeader(http.StatusOK)

	// Replay the events that already happened (in case the wizard
	// subscribed late) then stream new ones as they arrive.
	ch, history := runner.Subscribe()
	for _, ev := range history {
		writeSSE(w, ev)
	}
	flusher.Flush()

	// Keepalive pings so reverse proxies don't drop idle SSE conns.
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				// Runner finished — emit the "done" sentinel so the
				// client knows to stop reconnecting.
				_, _ = fmt.Fprintf(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func writeSSE(w http.ResponseWriter, ev setup.Event) {
	b, _ := json.Marshal(ev)
	_, _ = fmt.Fprintf(w, "event: line\ndata: %s\n\n", b)
}

// wizardServerVersion is the version string shown in the top-bar.
// Pulled from the same source as `dockmesh --version` — see version
// package — but we keep it indirect so the wizard handler doesn't
// need a dep on cmd/dockmesh.
func wizardServerVersion() string {
	return setupVersionFromGlobal
}

// setupVersionFromGlobal is set by main on init so the wizard top-bar
// shows the same version `dockmesh --version` reports. Avoids a
// circular import — main can reach into handlers, not the other way.
var setupVersionFromGlobal string

// SetSetupVersionString lets main register the version string at boot.
// Idempotent; called once before the HTTP server starts.
func SetSetupVersionString(v string) { setupVersionFromGlobal = v }

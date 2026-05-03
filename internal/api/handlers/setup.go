package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	writeJSON(w, http.StatusOK, h.SetupState.SnapshotStatus())
}

// SetupPreflight runs the host-fact + Docker-health checks the
// wizard's first page renders. Public — wizard runs before auth exists.
func (h *Handlers) SetupPreflight(w http.ResponseWriter, r *http.Request) {
	pf := setup.CollectPreflight(r.Context(), h.Docker)
	writeJSON(w, http.StatusOK, pf)
}

// SetupServerInfo feeds the wizard's top-bar (version · os · ip · docker).
// The wizard polls this every ~10s so uptime stays live while the
// operator is configuring. Lightweight on purpose.
func (h *Handlers) SetupServerInfo(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		URL          string `json:"url"`
		ExpectHealth bool   `json:"expect_health"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	writeJSON(w, http.StatusOK, setup.CheckURL(r.Context(), req.URL, req.ExpectHealth))
}

// SetupCommit kicks off the install. Returns 200 with a run_id; the
// real progress comes via /setup/stream/{run_id}. Decoupled so the
// frontend can subscribe to the stream BEFORE the runner finishes —
// the alternative (commit returns when done) would block the HTTP
// connection for several seconds and lose intermediate events.
func (h *Handlers) SetupCommit(w http.ResponseWriter, r *http.Request) {
	if h.SetupState == nil || !h.SetupState.Active() {
		writeError(w, http.StatusGone, "setup mode is no longer active")
		return
	}
	if h.SetupState.Expired() {
		writeError(w, http.StatusGone, "setup window expired — restart the dockmesh service to start a fresh window")
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

	runner := setup.NewRunner()
	runID := uuid.NewString()
	h.setupRuns.Store(runID, runner)

	go func() {
		// Detach from the request context so the install isn't
		// cancelled when the operator's commit POST returns. The
		// runner uses its own background context.
		ctx := context.Background()
		runner.Run(ctx, in, h.SetupCommit_)
		// Mark setup complete IF the runner ended without error AND
		// we're still in setup mode. Idempotent — multiple commits
		// after a successful one just re-mark it.
		if done, err := runner.Done(); done && err == nil && h.SetupState != nil {
			h.SetupState.Complete()
		}
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

package handlers

import (
	"context"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/pkg/version"
)

// pingCtx clamps the DB-ping to 2s so a slow / hung DB doesn't keep
// the readiness probe hanging past the load balancer's own timeout.
func pingCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Second)
}

var startedAt = time.Now()

// shuttingDown is flipped to 1 by the main loop when SIGTERM arrives,
// before the http.Server.Shutdown call starts. /healthz/ready reads
// this flag so a load balancer stops sending new traffic while the
// server drains in-flight connections. P.12.2.
var shuttingDown atomic.Bool

// MarkShuttingDown is called by main() the moment a shutdown signal
// is received. Idempotent.
func MarkShuttingDown() { shuttingDown.Store(true) }

// IsShuttingDown reports the current drain state — exported for
// anything else that wants to short-circuit (e.g. a metrics
// collector that stops scraping during shutdown).
func IsShuttingDown() bool { return shuttingDown.Load() }

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	// `docker` reflects the daemon connection state, not merely whether
	// the client object exists. With the auto-reconnect wrapper the
	// client is always non-nil; the live connection status comes from
	// the background probe and may flip true ↔ false over time. The
	// dashboard banner and macOS-boot-race UX both key off this flag.
	dockerOK := h.Docker != nil && h.Docker.Connected()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"docker":  dockerOK,
	})
}

// Live is the liveness probe. Always 200 if the Go process is alive
// enough to serve the request. Intentionally does NOT touch the DB,
// Docker, or any subsystem — a failing subsystem should be visible
// as a Ready failure, not a kill-the-pod-and-restart signal.
func (h *Handlers) Live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Ready is the readiness probe. Returns 200 only when the DB is
// pingable AND we're not in shutdown drain. 503 otherwise — which
// tells the load balancer to stop routing new traffic. P.12.2.
func (h *Handlers) Ready(w http.ResponseWriter, r *http.Request) {
	if shuttingDown.Load() {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	if h.DB != nil {
		ctx, cancel := pingCtx(r)
		defer cancel()
		if err := h.DB.PingContext(ctx); err != nil {
			http.Error(w, "db unreachable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// SystemInfo returns detailed server instance info for the System tab.
func (h *Handlers) SystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version.Version,
		"commit":     version.Commit,
		"build_date": version.Date,
		"go_version": runtime.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
	})
}

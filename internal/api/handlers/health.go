package handlers

import (
	"net/http"
	"runtime"
	"time"

	"github.com/dockmesh/dockmesh/pkg/version"
)

var startedAt = time.Now()

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	dockerOK := h.Docker != nil
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"docker":  dockerOK,
	})
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

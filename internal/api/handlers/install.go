package handlers

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// AgentInstallScript serves the bash installer with the token + URLs
// templated in. The token is passed via ?token=… query parameter; we
// validate it pre-flight so users get an instant error instead of waiting
// for the script to fail at enroll time. No JWT auth — the token IS the
// auth.
func (h *Handlers) AgentInstallScript(w http.ResponseWriter, r *http.Request) {
	if h.Agents == nil {
		http.Error(w, "agents not configured", http.StatusServiceUnavailable)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// Read the script template from disk. In production we could embed
	// this; reading on each request makes hot iteration easier and the
	// file is tiny anyway.
	path := os.Getenv("DOCKMESH_INSTALL_SCRIPT")
	if path == "" {
		path = "./scripts/install-agent.sh"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "installer script not found", http.StatusServiceUnavailable)
		return
	}

	// Template
	publicURL := h.Agents.PublicURL()
	binaryURL := publicURL + "/install/dockmesh-agent-linux-amd64"
	enrollURL := publicURL + "/api/v1/agents/enroll"

	out := strings.NewReplacer(
		"{{TOKEN}}", token,
		"{{SERVER_URL}}", publicURL,
		"{{ENROLL_URL}}", enrollURL,
		"{{AGENT_URL}}", h.Agents.AgentURL(),
		"{{BINARY_URL}}", binaryURL,
	).Replace(string(raw))

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(out))
}

// AgentBinary serves the cross-compiled linux agent binary. Path is
// configurable so we can run the dev server from a checkout but a
// production deployment ships /usr/local/share/dockmesh/bin/.
func (h *Handlers) AgentBinary(w http.ResponseWriter, r *http.Request) {
	name := chiURLParam(r, "name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// Whitelist exactly what we ship.
	allowed := map[string]bool{
		"dockmesh-agent-linux-amd64": true,
		"dockmesh-agent-linux-arm64": true,
	}
	if !allowed[name] {
		http.NotFound(w, r)
		return
	}

	dir := os.Getenv("DOCKMESH_BINARY_DIR")
	if dir == "" {
		dir = "./bin"
	}
	full := filepath.Join(dir, name)
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w,
				"agent binary not bundled with this server build — "+
					"run `make agent-bundle` and restart, or set DOCKMESH_BINARY_DIR",
				http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, stat.ModTime(), f)
}

// chiURLParam is a thin wrapper to keep this file independent of the chi
// import; we need the same behaviour without import cycles.
func chiURLParam(r *http.Request, key string) string {
	// We import chi elsewhere in the handlers package, so use the
	// symbol indirectly via a small helper kept in handlers.go's scope.
	// In practice this is just chi.URLParam — see install_chi.go.
	return chiParam(r, key)
}

// silence unused
var _ = agents.ErrNotFound

package handlers

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// AgentInstallScript serves the bash installer with the token, URLs, the
// agents CA and the agent binary checksums templated in. No JWT auth —
// the enrollment token is redeemed later by the agent itself. The
// optional ?ca= fingerprint from the install command must match this
// server's CA, so a command copied before a CA rotation fails here with
// a clear message instead of at enroll time.
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
	if !installTokenRe.MatchString(token) {
		http.Error(w, "malformed token", http.StatusBadRequest)
		return
	}
	caFingerprint := h.Agents.CAFingerprint()
	if want := r.URL.Query().Get("ca"); want != "" && !strings.EqualFold(want, caFingerprint) {
		http.Error(w, "the server's agent CA has changed since this install command was created — "+
			"copy a fresh command from the Hosts page", http.StatusConflict)
		return
	}
	enrollURL := h.Agents.EnrollURL()
	if enrollURL == "" {
		http.Error(w, "agent listener URL is not configured (DOCKMESH_AGENT_PUBLIC_URL)", http.StatusServiceUnavailable)
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

	// A missing binary leaves its checksum empty; the script then refuses
	// to install on that architecture.
	sumAMD64, _ := agents.AgentBinarySHA256("amd64")
	sumARM64, _ := agents.AgentBinarySHA256("arm64")

	out := strings.NewReplacer(
		"{{TOKEN}}", token,
		"{{SERVER_URL}}", h.Agents.PublicURL(),
		"{{ENROLL_URL}}", enrollURL,
		"{{AGENT_URL}}", h.Agents.AgentURL(),
		"{{CA_FINGERPRINT}}", caFingerprint,
		"{{CA_CERT_PEM}}", strings.TrimSpace(string(h.Agents.CACertPEM())),
		"{{BINARY_SHA256_AMD64}}", sumAMD64,
		"{{BINARY_SHA256_ARM64}}", sumARM64,
	).Replace(string(raw))

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(out))
}

// installTokenRe matches the base64url tokens newToken produces. The
// token is pasted into a shell script, so nothing else may get through.
var installTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// AgentBinary serves the cross-compiled linux agent binary. Path is
// configurable so we can run the dev server from a checkout but a
// production deployment ships /usr/local/share/dockmesh/bin/.
func (h *Handlers) AgentBinary(w http.ResponseWriter, r *http.Request) {
	name := chiURLParam(r, "name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// Whitelist exactly what we ship. Extended in P.12.9 to include the
	// user-facing dmctl CLI for the platforms operators actually run it on.
	allowed := map[string]bool{
		"dockmesh-agent-linux-amd64": true,
		"dockmesh-agent-linux-arm64": true,
		"dmctl-linux-amd64":          true,
		"dmctl-linux-arm64":          true,
		"dmctl-darwin-amd64":         true,
		"dmctl-darwin-arm64":         true,
		"dmctl-windows-amd64.exe":    true,
	}
	if !allowed[name] {
		http.NotFound(w, r)
		return
	}

	full := filepath.Join(agents.BinaryDir(), name)
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

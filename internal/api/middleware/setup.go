package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/setup"
)

// SetupCookie carries the setup session a right setup code opens.
const SetupCookie = "dm_setup"

// SetupToken returns the setup session token of r, empty without one.
func SetupToken(r *http.Request) string {
	c, err := r.Cookie(SetupCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// staticExts are the files the SvelteKit shell loads from the site root.
var staticExts = []string{".svg", ".png", ".ico", ".woff", ".woff2", ".css", ".js", ".map", ".webmanifest"}

// openDuringSetup is what answers before a setup code was entered: the
// code form (the /setup shell and its static assets), the code endpoint,
// the bare status and the health probes. This list is never switchable
// off; TestSetupModeRefusesEveryRouteWithoutSession walks the router
// against it.
func openDuringSetup(method, path string) bool {
	read := method == http.MethodGet || method == http.MethodHead
	switch path {
	case "/api/v1/setup/status", "/api/v1/health", "/healthz/live", "/healthz/ready":
		return read
	case "/api/v1/setup/code":
		return method == http.MethodPost
	case "/setup", "/favicon.ico", "/robots.txt":
		return read
	}
	if !read || strings.HasPrefix(path, "/api/") {
		return false
	}
	if strings.HasPrefix(path, "/_app/") {
		return true
	}
	for _, ext := range staticExts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// readOnlySetupPath is what a session that started the install may still
// call after the commit or the end of its code: progress and result.
func readOnlySetupPath(method, path string) bool {
	return method == http.MethodGet && strings.HasPrefix(path, "/api/v1/setup/stream/")
}

func isSetupAPI(path string) bool {
	return path == "/api/v1/setup" || strings.HasPrefix(path, "/api/v1/setup/")
}

// isWizardOnlyPath identifies routes that ONLY make sense while setup
// is active. Once setup is complete we want to lock these so the
// operator can't accidentally re-run the wizard on a configured
// server (which would fail anyway, but the UI shouldn't even render).
// /api/v1/setup/status is intentionally excluded — the SvelteKit root
// layout probes it on every page load to decide whether to redirect.
func isWizardOnlyPath(path string) bool {
	if path == "/setup" || strings.HasPrefix(path, "/setup/") {
		return true
	}
	// The run stream replays a finished install to a wizard that subscribed
	// late; a fast install ends setup before the EventSource connects.
	if path == "/api/v1/setup/status" || strings.HasPrefix(path, "/api/v1/setup/stream/") {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/setup/") || path == "/api/v1/setup" {
		return true
	}
	return false
}

// SetupGate refuses requests while setup mode is active unless they are
// open before the code or carry a setup session allowed to make them, and
// refuses wizard-only paths once setup is complete.
func SetupGate(state *setup.State) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if state != nil && state.Active() {
				if openDuringSetup(r.Method, path) {
					next.ServeHTTP(w, r)
					return
				}
				sess := state.Codes().Session(SetupToken(r))
				switch {
				case isSetupAPI(path) && sess == setup.SessionWrite,
					isSetupAPI(path) && sess == setup.SessionReadOnly && readOnlySetupPath(r.Method, path):
					next.ServeHTTP(w, r)
				case !strings.HasPrefix(path, "/api/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
					http.Redirect(w, r, "/setup", http.StatusSeeOther)
				case sess == setup.SessionNone || sess == setup.SessionReplaced:
					refuseSetup(w, http.StatusUnauthorized, "setup code required")
				default:
					refuseSetup(w, http.StatusServiceUnavailable, "server is in setup mode — finish the install wizard first")
				}
				return
			}
			if isWizardOnlyPath(path) {
				if strings.HasPrefix(path, "/setup") {
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "setup mode is no longer active",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func refuseSetup(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "setup_url": "/setup"})
}

// SetupHeader is the request header the install wizard sends on every
// state-changing /setup call. A page on another origin cannot set it
// without a CORS preflight, which the API never grants, so a visitor's
// browser cannot be steered into running or probing the setup.
const SetupHeader = "X-Dockmesh-Setup"

// RequireSetupHeader rejects requests without SetupHeader.
func RequireSetupHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sec-Fetch-Site: a browser marks cross-site requests itself, so
		// a page can't get past this even where CORS would let it.
		if r.Header.Get(SetupHeader) == "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing " + SetupHeader + " header"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

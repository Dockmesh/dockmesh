package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/setup"
)

// alwaysAllowedPaths are reachable even while setup mode is active.
// The wizard itself + the assets it needs to render + healthchecks +
// the OpenAPI spec the wizard frontend reads to know endpoint shapes.
//
// Anything else returns 503 with a body pointing at /setup so a
// legitimate UI client can redirect there instead of trying to login
// against an unconfigured server.
var alwaysAllowedPaths = []string{
	"/setup",            // wizard UI shell
	"/setup/",           // wizard UI sub-routes
	"/api/v1/setup/",    // wizard API
	"/api/v1/setup",     // wizard API root
	"/api/v1/health",    // health probe
	"/api/v1/openapi",   // openapi.json / .yaml
	"/api/v1/docs",      // swagger ui
	"/healthz/live",     // k8s probe
	"/healthz/ready",    // k8s probe
	"/_app/",            // sveltekit assets
	"/favicon.ico",
	"/robots.txt",
}

// allowedDuringSetup returns true if the path is part of the set the
// wizard needs to function. Compared as prefix-match for the directory
// entries (`/setup/`, `/_app/`) and exact-match for files.
func allowedDuringSetup(path string) bool {
	for _, p := range alwaysAllowedPaths {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
		} else {
			if path == p {
				return true
			}
		}
	}
	// Root path is special — when setup is active we still want to
	// serve the SvelteKit shell at "/" so the client-side router can
	// redirect to /setup. Without this the operator hitting the bare
	// dashboard URL would get a 503 with no UI.
	if path == "/" {
		return true
	}
	// Static assets the SvelteKit shell + wizard load directly from the
	// site root (logo, favicons, fonts, robots). Keep the list of
	// extensions narrow so we can't accidentally let API responses
	// through.
	for _, ext := range []string{".svg", ".png", ".ico", ".woff", ".woff2", ".css", ".js", ".map", ".webmanifest"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
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
	if path == "/api/v1/setup/status" {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/setup/") || path == "/api/v1/setup" {
		return true
	}
	return false
}

// SetupGate refuses requests to non-setup paths while setup mode is
// active, AND refuses requests to wizard-only paths once setup is
// complete. UI clients see a 503 (during) or a 410/redirect (after);
// non-UI clients get a clear error rather than a generic 401.
func SetupGate(state *setup.State) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			active := state != nil && state.Active()
			if active {
				if allowedDuringSetup(r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":     "server is in setup mode — finish the install wizard first",
					"setup_url": "/setup",
				})
				return
			}
			if isWizardOnlyPath(r.URL.Path) {
				if strings.HasPrefix(r.URL.Path, "/setup") {
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

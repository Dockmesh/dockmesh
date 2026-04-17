package openapi_test

// TestOpenAPIDriftAgainstRoutes is the mechanical enforcement behind
// the "every handler change updates openapi.yaml" rule documented in
// CLAUDE.md. It builds the real API router (via api.NewRouter), walks
// every registered chi route, and cross-checks against the operations
// declared in internal/api/openapi/openapi.yaml.
//
// Failures are caught in two directions:
//
//   1. A route exists but has no matching operation in the spec
//      (handler was added without updating the spec — the common
//      drift direction).
//   2. The spec declares an operation that has no matching route
//      (spec is stale — endpoint removed but YAML still lists it).
//
// Some routes are legitimately undocumented — /health, /openapi.json,
// /docs, fallback routes, the OIDC-cookie callback. Those are listed
// in the undocumentedRoutes allow-list below, with a one-line reason
// per entry. Adding a new entry to that list requires justification
// in the PR description — it is the escape hatch, not the norm.
//
// When this test turns red: fix the spec. DO NOT add the route to the
// allow-list unless the PR description explains why it's legitimately
// undocumented.

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/openapi"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/go-chi/chi/v5"
)

// undocumentedRoutes lists operations that legitimately don't appear
// in openapi.yaml. Keep this list short and explain each entry.
//
// The long list below is P.11.10's "initial migration scaffolding" —
// when we shipped the skeleton + tooling commit, 159 existing routes
// pre-dated the spec. Each subsequent endpoint-group commit adds the
// group's operations to openapi.yaml and removes those lines from
// here. When the list shrinks to only the four genuinely-undocumented
// entries at the top (health + the spec endpoints themselves), the
// initial migration is complete. Until then a shrinking allow-list
// is the progress indicator.
//
// **Do not add new entries below the "initial migration" header for
// new endpoints.** New endpoints must go directly into openapi.yaml.
var undocumentedRoutes = map[string]bool{
	// -- Genuinely undocumented (permanent) --
	// Health probe — pure liveness, not an API endpoint.
	"GET /api/v1/health": true,
	// The spec endpoints themselves — we don't declare them inside the
	// spec (too meta, and browsers fetching swagger UI would recurse).
	"GET /api/v1/openapi.json": true,
	"GET /api/v1/openapi.yaml": true,
	"GET /api/v1/docs":         true,

	// -- Initial migration scaffolding (P.11.10) --
	// Shrink by moving each entry into openapi.yaml, per endpoint group.
	"DELETE /api/v1/agents/{id}":                          true,
	"DELETE /api/v1/alerts/rules/{id}":                    true,
	"DELETE /api/v1/backups/jobs/{id}":                    true,
	"DELETE /api/v1/backups/targets/{id}":                 true,
	"DELETE /api/v1/containers/{id}":                      true,
	"DELETE /api/v1/global-env/{id}":                      true,
	"DELETE /api/v1/hosts/{id}/tags/{tag}":                true,
	"DELETE /api/v1/images/{id}":                          true,
	"DELETE /api/v1/mfa":                                  true,
	"DELETE /api/v1/networks/{id}":                        true,
	"DELETE /api/v1/notifications/channels/{id}":          true,
	"DELETE /api/v1/oidc/providers/{id}":                  true,
	"DELETE /api/v1/proxy/routes/{id}":                    true,
	"DELETE /api/v1/roles/{name}":                         true,
	"DELETE /api/v1/settings/api-tokens/{id}":             true,
	"DELETE /api/v1/settings/registries/{id}":             true,
	"DELETE /api/v1/stacks/{name}":                        true,
	"DELETE /api/v1/stacks/{name}/migrate/{id}/source":    true,
	"DELETE /api/v1/stacks/{name}/scaling-rules":          true,
	"DELETE /api/v1/users/{id}":                           true,
	"DELETE /api/v1/users/{id}/mfa":                       true,
	"DELETE /api/v1/volumes/{name}":                       true,
	"GET /api/v1/agents":                                  true,
	"GET /api/v1/agents/{id}":                             true,
	"GET /api/v1/alerts/history":                          true,
	"GET /api/v1/alerts/rules":                            true,
	"GET /api/v1/audit":                                   true,
	"GET /api/v1/audit/verify":                            true,
	"GET /api/v1/auth/oidc/providers":                     true,
	"GET /api/v1/auth/oidc/{slug}/callback":               true,
	"GET /api/v1/auth/oidc/{slug}/login":                  true,
	"GET /api/v1/backups/jobs":                            true,
	"GET /api/v1/backups/jobs/{id}":                       true,
	"GET /api/v1/backups/runs":                            true,
	"GET /api/v1/backups/targets":                         true,
	"GET /api/v1/containers":                              true,
	"GET /api/v1/containers/{id}":                         true,
	"GET /api/v1/containers/{id}/metrics":                 true,
	"GET /api/v1/containers/{id}/update-history":          true,
	"GET /api/v1/containers/{id}/update-info":             true,
	"GET /api/v1/global-env":                              true,
	"GET /api/v1/global-env/groups":                       true,
	"GET /api/v1/hosts":                                   true,
	"GET /api/v1/hosts/tags/all":                          true,
	"GET /api/v1/hosts/{id}/drain/{drain_id}":             true,
	"GET /api/v1/hosts/{id}/tags":                         true,
	"GET /api/v1/images":                                  true,
	"GET /api/v1/images/{id}/scan":                        true,
	"GET /api/v1/me":                                      true,
	"GET /api/v1/migrations":                              true,
	"GET /api/v1/migrations/active":                       true,
	"GET /api/v1/networks":                                true,
	"GET /api/v1/networks/topology":                       true,
	"GET /api/v1/networks/{id}":                           true,
	"GET /api/v1/notifications/channels":                  true,
	"GET /api/v1/oidc/providers":                          true,
	"GET /api/v1/proxy/routes":                            true,
	"GET /api/v1/proxy/status":                            true,
	"GET /api/v1/roles":                                   true,
	"GET /api/v1/roles/permissions":                       true,
	"GET /api/v1/roles/{name}":                            true,
	"GET /api/v1/settings":                                true,
	"GET /api/v1/settings/api-tokens":                     true,
	"GET /api/v1/settings/registries":                     true,
	"GET /api/v1/stacks":                                  true,
	"GET /api/v1/stacks/{name}":                           true,
	"GET /api/v1/stacks/{name}/migrate/{id}":              true,
	"GET /api/v1/stacks/{name}/scale":                     true,
	"GET /api/v1/stacks/{name}/scaling-rules":             true,
	"GET /api/v1/stacks/{name}/services/{service}/scale":  true,
	"GET /api/v1/stacks/{name}/status":                    true,
	"GET /api/v1/system/backup-status":                    true,
	"GET /api/v1/system/info":                             true,
	"GET /api/v1/system/metrics":                          true,
	"GET /api/v1/users":                                   true,
	"GET /api/v1/volumes":                                 true,
	"GET /api/v1/volumes/{name}":                          true,
	"GET /api/v1/volumes/{name}/browse":                   true,
	"GET /api/v1/volumes/{name}/browse/file":              true,
	"GET /api/v1/ws/events":                               true,
	"GET /api/v1/ws/exec/{id}":                            true,
	"GET /api/v1/ws/logs/{id}":                            true,
	"GET /api/v1/ws/stats/{id}":                           true,
	"POST /api/v1/agents":                                 true,
	"POST /api/v1/agents/enroll":                          true,
	"POST /api/v1/agents/{id}/upgrade":                    true,
	"POST /api/v1/alerts/rules":                           true,
	"POST /api/v1/auth/login":                             true,
	"POST /api/v1/auth/logout":                            true,
	"POST /api/v1/auth/mfa":                               true,
	"POST /api/v1/auth/refresh":                           true,
	"POST /api/v1/backups/jobs":                           true,
	"POST /api/v1/backups/jobs/{id}/run":                  true,
	"POST /api/v1/backups/runs/{id}/restore":              true,
	"POST /api/v1/backups/targets":                        true,
	"POST /api/v1/backups/targets/discover-shares":        true,
	"POST /api/v1/backups/targets/test-config":            true,
	"POST /api/v1/backups/targets/{id}/test":              true,
	"POST /api/v1/containers/{id}/kill":                   true,
	"POST /api/v1/containers/{id}/pause":                  true,
	"POST /api/v1/containers/{id}/restart":                true,
	"POST /api/v1/containers/{id}/rollback":               true,
	"POST /api/v1/containers/{id}/start":                  true,
	"POST /api/v1/containers/{id}/stop":                   true,
	"POST /api/v1/containers/{id}/unpause":                true,
	"POST /api/v1/containers/{id}/update":                 true,
	"POST /api/v1/convert/run-to-compose":                 true,
	"POST /api/v1/global-env":                             true,
	"POST /api/v1/hosts/{id}/drain/execute":               true,
	"POST /api/v1/hosts/{id}/drain/plan":                  true,
	"POST /api/v1/hosts/{id}/drain/{drain_id}/abort":      true,
	"POST /api/v1/hosts/{id}/drain/{drain_id}/pause":      true,
	"POST /api/v1/hosts/{id}/drain/{drain_id}/resume":     true,
	"POST /api/v1/hosts/{id}/tags":                        true,
	"POST /api/v1/images/prune":                           true,
	"POST /api/v1/images/pull":                            true,
	"POST /api/v1/images/{id}/scan":                       true,
	"POST /api/v1/mfa/enroll/start":                       true,
	"POST /api/v1/mfa/enroll/verify":                      true,
	"POST /api/v1/networks":                               true,
	"POST /api/v1/networks/prune":                         true,
	"POST /api/v1/notifications/channels":                 true,
	"POST /api/v1/notifications/channels/{id}/test":       true,
	"POST /api/v1/oidc/providers":                         true,
	"POST /api/v1/oidc/providers/reload":                  true,
	"POST /api/v1/proxy/disable":                          true,
	"POST /api/v1/proxy/enable":                           true,
	"POST /api/v1/proxy/routes":                           true,
	"POST /api/v1/roles":                                  true,
	"POST /api/v1/settings/api-tokens":                    true,
	"POST /api/v1/settings/registries":                    true,
	"POST /api/v1/settings/registries/{id}/test":          true,
	"POST /api/v1/stacks":                                 true,
	"POST /api/v1/stacks/{name}/deploy":                   true,
	"POST /api/v1/stacks/{name}/migrate":                  true,
	"POST /api/v1/stacks/{name}/migrate/preflight":        true,
	"POST /api/v1/stacks/{name}/migrate/{id}/rollback":    true,
	"POST /api/v1/stacks/{name}/services/{service}/scale": true,
	"POST /api/v1/stacks/{name}/stop":                     true,
	"POST /api/v1/users":                                  true,
	"POST /api/v1/volumes":                                true,
	"POST /api/v1/volumes/prune":                          true,
	"POST /api/v1/ws/ticket":                              true,
	"PUT /api/v1/alerts/rules/{id}":                       true,
	"PUT /api/v1/backups/jobs/{id}":                       true,
	"PUT /api/v1/backups/system/enabled":                  true,
	"PUT /api/v1/backups/targets/{id}":                    true,
	"PUT /api/v1/global-env/{id}":                         true,
	"PUT /api/v1/hosts/{id}/tags":                         true,
	"PUT /api/v1/notifications/channels/{id}":             true,
	"PUT /api/v1/oidc/providers/{id}":                     true,
	"PUT /api/v1/proxy/routes/{id}":                       true,
	"PUT /api/v1/roles/{name}":                            true,
	"PUT /api/v1/settings":                                true,
	"PUT /api/v1/settings/registries/{id}":                true,
	"PUT /api/v1/stacks/{name}":                           true,
	"PUT /api/v1/stacks/{name}/scaling-rules":             true,
	"PUT /api/v1/users/{id}":                              true,
	"PUT /api/v1/users/{id}/password":                     true,
}

func TestOpenAPIDriftAgainstRoutes(t *testing.T) {
	spec, err := openapi.Load()
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}

	// Build a router with nil-safe handlers — we only need the chi
	// routing tree, not functional endpoints.
	emptyFS := fs.FS(nil)
	h := handlers.New(handlers.Deps{
		// All nil is fine; Handlers only stores pointers and the
		// router constructor doesn't call into them.
		Auth: (*auth.Service)(nil),
	})
	router := api.NewRouter(h, nil, emptyFS, false)

	// Collect every registered (method, pattern) under /api/v1.
	type op struct{ Method, Path string }
	var registered []op
	walkErr := chi.Walk(router.(chi.Router), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Only check /api/v1 routes — the spec's `servers:` entry
		// uses /api/v1 as the base.
		if !strings.HasPrefix(route, "/api/v1") {
			return nil
		}
		registered = append(registered, op{Method: method, Path: route})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("chi.Walk: %v", walkErr)
	}

	// Build a set from the spec. Spec paths are relative to the servers
	// entry (which is /api/v1), so we prefix them for comparison.
	specSet := map[string]bool{}
	for _, o := range spec.Operations() {
		specSet["/api/v1"+o.Path+"|"+o.Method] = true
	}

	// Build a set of registered routes too.
	routeSet := map[string]bool{}
	for _, r := range registered {
		routeSet[r.Path+"|"+r.Method] = true
	}

	// Direction 1: registered routes missing from the spec.
	var missingFromSpec []string
	for _, r := range registered {
		key := r.Method + " " + r.Path
		if undocumentedRoutes[key] {
			continue
		}
		if !specSet[r.Path+"|"+r.Method] {
			missingFromSpec = append(missingFromSpec, key)
		}
	}

	// Direction 2: spec operations with no matching route.
	var staleInSpec []string
	for _, o := range spec.Operations() {
		fullPath := "/api/v1" + o.Path
		key := o.Method + " " + fullPath
		if undocumentedRoutes[key] {
			continue
		}
		if !routeSet[fullPath+"|"+o.Method] {
			staleInSpec = append(staleInSpec, key)
		}
	}

	if len(missingFromSpec) == 0 && len(staleInSpec) == 0 {
		return
	}

	// Produce the actionable error message — tells the developer
	// exactly which file and which direction needs a change.
	var b strings.Builder
	b.WriteString("OpenAPI spec drift detected. See CLAUDE.md \"OpenAPI Contract\".\n")
	b.WriteString("Source of truth: internal/api/openapi/openapi.yaml\n\n")
	if len(missingFromSpec) > 0 {
		b.WriteString("Handlers exist but spec has no matching operation:\n")
		for _, s := range missingFromSpec {
			b.WriteString("  + add to spec: ")
			b.WriteString(s)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(staleInSpec) > 0 {
		b.WriteString("Spec declares operations that no longer exist in the router:\n")
		for _, s := range staleInSpec {
			b.WriteString("  - remove from spec: ")
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	t.Fatal(b.String())
}

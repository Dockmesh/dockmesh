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
// in openapi.yaml. Keep this list short — each entry is a spec hole,
// and every hole should have a one-line justification.
//
// The P.11.10 initial migration is complete: all 151 routes that
// pre-dated the spec are now documented. Only the four entries below
// remain, and none of them should grow into a pattern.
//
// **Do not add new entries here for new endpoints.** New routes must
// go directly into openapi.yaml, per the rule in CLAUDE.md
// ("OpenAPI Contract"). If you find yourself wanting to add to this
// list, it's almost certainly the wrong move — document the endpoint
// instead.
var undocumentedRoutes = map[string]bool{
	// Health probe — pure liveness, not an API endpoint.
	"GET /api/v1/health": true,
	// The spec endpoints themselves — we don't declare them inside the
	// spec (too meta, and browsers fetching swagger UI would recurse).
	"GET /api/v1/openapi.json": true,
	"GET /api/v1/openapi.yaml": true,
	"GET /api/v1/docs":         true,
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

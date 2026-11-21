package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A stack whose deployment targets a Swarm cluster is in the cluster's
// scope and in a stack scope on that cluster, never in a host scope, and
// host-only actions refuse it instead of running it on a host.
func TestStackOnClusterTarget(t *testing.T) {
	e := newScopeEnv(t)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/api/v1/stacks/{name}/deploy", e.h.DeployStack)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/api/v1/stacks/{name}/stop", e.h.StopStack)
		r.With(middleware.RequirePerm(rbac.PermStacksDelete)).Delete("/api/v1/stacks/{name}", e.h.DeleteStack)
		r.With(middleware.RequirePerm(rbac.PermStacksView)).Get("/api/v1/stacks/{name}/cleanup-preview", e.h.CleanupPreview)
	})
	if _, err := e.db.Exec(`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('swarm', 'cluster', 'c1', '')`); err != nil {
		t.Fatal(err)
	}
	perms := []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy, rbac.PermStacksDelete}
	e.role("on-local", perms, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	e.role("on-c1", perms, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("stack-on-c1", perms, rbac.Scope{Type: rbac.ScopeStack, Host: "c1", Value: "swarm"})
	e.role("stack-on-local", perms, rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "swarm"})
	e.user("root", "admin")
	for _, u := range []string{"on-local", "on-c1", "stack-on-c1", "stack-on-local"} {
		e.user("u-"+u, u)
	}

	cases := []struct {
		method, user, path string
		want               int
	}{
		{http.MethodDelete, "root", "/api/v1/stacks/swarm?stop=true&volumes=true&networks=true&images=true", http.StatusConflict},
		{http.MethodDelete, "u-on-c1", "/api/v1/stacks/swarm?stop=true&volumes=true", http.StatusConflict},
		{http.MethodGet, "root", "/api/v1/stacks/swarm/cleanup-preview", http.StatusConflict},
		{http.MethodPost, "root", "/api/v1/stacks/swarm/deploy", http.StatusConflict},
		{http.MethodPost, "root", "/api/v1/stacks/swarm/deploy?host=local", http.StatusConflict},
		{http.MethodPost, "root", "/api/v1/stacks/swarm/stop", http.StatusConflict},
		{http.MethodPost, "u-on-c1", "/api/v1/stacks/swarm/deploy", http.StatusConflict},
		{http.MethodPost, "u-stack-on-c1", "/api/v1/stacks/swarm/deploy", http.StatusConflict},
		{http.MethodPost, "u-on-local", "/api/v1/stacks/swarm/deploy", http.StatusForbidden},
		{http.MethodPost, "u-stack-on-local", "/api/v1/stacks/swarm/deploy", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.user+" "+tc.path, func(t *testing.T) {
			rr := e.do(tc.method, tc.path, e.login(tc.user).AccessToken, nil)
			if rr.Code != tc.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tc.want, rr.Body)
			}
			if tc.want != http.StatusConflict {
				return
			}
			var body struct{ Code string }
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Code != "stack_on_cluster" {
				t.Fatalf("body %s, want code stack_on_cluster", rr.Body)
			}
		})
	}
}

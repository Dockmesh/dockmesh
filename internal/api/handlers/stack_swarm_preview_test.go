package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// The preview needs deploy rights on the stack on the cluster plus the
// right to read the stack's env where it is now, because its specs carry
// the env values.
func TestSwarmPreviewAccess(t *testing.T) {
	e := newScopeEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	e.h.Stacks = mgr
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Get("/api/v1/stacks/{name}/swarm-preview", e.h.SwarmPreview)
	})
	root := e.user("root", "admin")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: root.ID, Access: e.h.Roles.AccessFor(root.ID)})
	if rr := clusterCall(ctx, e.h.CreateCluster, http.MethodPost, createClusterRequest{HostID: "local", Name: "prod"}); rr.Code != http.StatusCreated {
		t.Fatalf("create cluster %d: %s", rr.Code, rr.Body)
	}
	if _, err := e.h.Clusters.Get(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"onc1":   "services:\n  web:\n    image: nginx:1.29-alpine\n    environment:\n      TOKEN: ${TOKEN}\n",
		"onhost": "services:\n  web:\n    image: nginx:1.29-alpine\n",
		"bad":    "services:\n  web:\n    image: nginx:1.29-alpine\n    privileged: true\n    volumes: [./data:/data]\n",
	} {
		if _, err := mgr.Create(name, src, "TOKEN=s3cret\n"); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('onc1', 'cluster', 'c1', '')`,
		`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('onhost', 'host', 'local', 'local')`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	perms := []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}
	e.role("on-c1", perms, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("stack-on-c1", perms, rbac.Scope{Type: rbac.ScopeStack, Host: "c1", Value: "onc1"})
	e.role("on-local", perms, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	e.role("view-only", []rbac.Perm{rbac.PermStacksView})
	for _, u := range []string{"on-c1", "stack-on-c1", "on-local", "view-only"} {
		e.user("u-"+u, u)
	}

	cases := []struct {
		user, path string
		want       int
	}{
		{"root", "/api/v1/stacks/onc1/swarm-preview?cluster=c1", http.StatusOK},
		{"root", "/api/v1/stacks/onc1/swarm-preview", http.StatusBadRequest},
		{"root", "/api/v1/stacks/onc1/swarm-preview?cluster=nope", http.StatusNotFound},
		{"root", "/api/v1/stacks/missing/swarm-preview?cluster=c1", http.StatusNotFound},
		{"root", "/api/v1/stacks/onc1/swarm-preview?cluster=c1&environment=prod", http.StatusConflict},
		{"u-on-c1", "/api/v1/stacks/onc1/swarm-preview?cluster=c1", http.StatusOK},
		{"u-stack-on-c1", "/api/v1/stacks/onc1/swarm-preview?cluster=c1", http.StatusOK},
		{"u-on-local", "/api/v1/stacks/onc1/swarm-preview?cluster=c1", http.StatusForbidden},
		{"u-view-only", "/api/v1/stacks/onc1/swarm-preview?cluster=c1", http.StatusForbidden},
		// Deploy rights on the cluster don't open the env of a stack that
		// runs on a host outside the caller's scope.
		{"u-on-c1", "/api/v1/stacks/onhost/swarm-preview?cluster=c1", http.StatusForbidden},
		{"u-stack-on-c1", "/api/v1/stacks/onhost/swarm-preview?cluster=c1", http.StatusForbidden},
		{"u-on-local", "/api/v1/stacks/onhost/swarm-preview?cluster=c1", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.user+" "+tc.path, func(t *testing.T) {
			rr := e.do(http.MethodGet, tc.path, e.login(tc.user).AccessToken, nil)
			if rr.Code != tc.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tc.want, rr.Body)
			}
		})
	}

	var ok swarmPreviewResponse
	rr := e.do(http.MethodGet, "/api/v1/stacks/onc1/swarm-preview?cluster=c1", e.login("root").AccessToken, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &ok); err != nil {
		t.Fatal(err)
	}
	if !ok.Deployable || len(ok.Services) != 1 || ok.Services[0].Name != "onc1_web" ||
		len(ok.Services[0].TaskTemplate.ContainerSpec.Env) != 1 || ok.Services[0].TaskTemplate.ContainerSpec.Env[0] != "TOKEN=s3cret" {
		t.Fatalf("preview %s", rr.Body)
	}

	var bad swarmPreviewResponse
	rr = e.do(http.MethodGet, "/api/v1/stacks/bad/swarm-preview?cluster=c1", e.login("root").AccessToken, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &bad); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, f := range bad.Refusals {
		keys[f.Key] = true
	}
	if bad.Deployable || !keys["privileged"] || !keys["volumes /data"] {
		t.Fatalf("preview %s", rr.Body)
	}
}

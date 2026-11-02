package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/stacks/adopt"
)

// A host-scoped creator names the host a stack is for and can then use
// the stack while it is undeployed; a stack name someone else holds a
// stack assignment for can't be taken by a host-scoped creator.
func TestCreateStackForHost(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.h.Stacks = mgr
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksCreate)).Post("/stacks", e.h.CreateStack)
	})
	e.role("local-op", []rbac.Perm{rbac.PermStacksCreate, rbac.PermStacksView}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	e.user("loc", "local-op")
	e.user("root", "admin")
	owner := e.user("owner", "")
	if _, err := e.h.Roles.AddAssignment(ctx, rbac.Assignment{UserID: owner.ID, Role: "viewer", Scope: rbac.Scope{Type: rbac.ScopeStack, Value: "reserved"}}); err != nil {
		t.Fatal(err)
	}
	loc, root := e.login("loc").AccessToken, e.login("root").AccessToken
	compose := "services:\n  a:\n    image: busybox\n"

	tests := []struct {
		name, bearer, stack, host string
		want                      int
	}{
		{"own host", loc, "mine", "local", http.StatusCreated},
		{"no host", loc, "nohost", "", http.StatusForbidden},
		{"unknown host", loc, "other", "agent-x", http.StatusBadRequest},
		{"reserved name", loc, "reserved", "local", http.StatusConflict},
		{"admin takes a reserved name", root, "reserved", "", http.StatusCreated},
		{"admin without host", root, "plain", "", http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{"name": tt.stack, "compose": compose}
			if tt.host != "" {
				body["host_id"] = tt.host
			}
			if rr := e.do(http.MethodPost, "/stacks", tt.bearer, body); rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
	if _, err := mgr.Get("nohost"); err == nil {
		t.Error("stack created despite the denial")
	}
	if got, _ := e.h.Deployments.Target(ctx, "mine"); got != "local" {
		t.Errorf("target host = %q, want local", got)
	}
	if rr := e.do(http.MethodGet, "/api/v1/stacks/mine", loc, nil); rr.Code != http.StatusNoContent {
		t.Errorf("creator reading its undeployed stack: %d", rr.Code)
	}
	if rr := e.do(http.MethodGet, "/api/v1/stacks/plain", loc, nil); rr.Code != http.StatusForbidden {
		t.Errorf("host-scoped caller reading a stack without host: %d, want 403", rr.Code)
	}
}

// Discover lists only the projects the caller could adopt: a holder of a
// stack assignment sees that project, not every project on the host.
func TestDiscoverFiltersByStackScope(t *testing.T) {
	e := newScopeEnv(t)
	e.role("adopt-web", []rbac.Perm{rbac.PermStacksAdopt}, rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "web"})
	ids := map[string]string{"webby": e.user("webby", "adopt-web").ID, "root": e.user("root", "admin").ID}
	found := []adopt.DiscoveredStack{{ProjectName: "web", HostID: "local"}, {ProjectName: "db", HostID: "local"}}

	tests := []struct {
		user string
		want []string
	}{
		{"webby", []string{"web"}},
		{"root", []string{"web", "db"}},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/stacks/discover?host=local", nil)
			id := ids[tt.user]
			ctx := middleware.WithIdentity(req.Context(), middleware.Identity{UserID: id, Access: e.h.Roles.AccessFor(id)})
			req = req.WithContext(middleware.WithRoutePerm(ctx, rbac.PermStacksAdopt))
			var got []string
			for _, d := range e.h.filterDiscovered(req, found) {
				got = append(got, d.ProjectName)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

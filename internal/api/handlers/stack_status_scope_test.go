package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A host-scoped caller polls the status of a stack it just created: the
// stack runs nowhere yet, so it gets an empty status, not a 403 for the
// default host. A deployed stack on a host outside the scope stays 403.
func TestStackStatusOfUndeployedStackForScopedCaller(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksView)).Get("/api/v1/stacks/{name}/handler/status", e.h.StackStatus)
	})
	tagged := e.user("tagged", "viewer")
	e.scopeUser(tagged.ID, "prod")
	if err := e.h.Deployments.Set(ctx, "elsewhere", "local", "running"); err != nil {
		t.Fatal(err)
	}
	tok := e.login("tagged").AccessToken
	tests := []struct {
		stack string
		want  int
	}{
		{"fresh", http.StatusOK},
		{"elsewhere", http.StatusForbidden},
	}
	for _, tt := range tests {
		rr := e.do(http.MethodGet, "/api/v1/stacks/"+tt.stack+"/handler/status", tok, nil)
		if rr.Code != tt.want {
			t.Errorf("%s: %d, want %d: %s", tt.stack, rr.Code, tt.want, rr.Body)
		}
	}
}

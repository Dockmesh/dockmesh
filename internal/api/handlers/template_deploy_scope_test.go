package handlers

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/templates"
)

func TestTemplateDeployEnforcesScope(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.h.Stacks = mgr
	e.h.HostTags = hosttags.New(e.db)
	e.h.Templates = templates.New(e.db)
	tpl, err := e.h.Templates.Create(ctx, templates.Input{
		Slug: "tiny", Name: "Tiny", Compose: "services:\n  a:\n    image: busybox\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/templates/{id}/deploy", e.h.DeployTemplate)
	})

	deployPerms := []rbac.Perm{rbac.PermStacksDeploy, rbac.PermStacksView}
	e.role("elsewhere", deployPerms, rbac.Scope{ScopeType: "host", ScopeValue: "other-host"})
	e.user("hostscoped", "elsewhere")
	tagged := e.user("tagged", "operator")
	e.scopeUser(tagged.ID, "prod")

	path := "/templates/" + strconv.FormatInt(tpl.ID, 10) + "/deploy"
	tests := []struct{ user, stack string }{
		{"hostscoped", "t-role"},
		{"tagged", "t-tag"},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			rr := e.do(http.MethodPost, path, e.login(tt.user).AccessToken, map[string]any{"stack_name": tt.stack, "host_id": "local"})
			if rr.Code != http.StatusForbidden {
				t.Fatalf("got %d, want 403: %s", rr.Code, rr.Body)
			}
			if _, err := mgr.Get(tt.stack); err == nil {
				t.Fatalf("stack %s was created despite the denial", tt.stack)
			}
		})
	}
}

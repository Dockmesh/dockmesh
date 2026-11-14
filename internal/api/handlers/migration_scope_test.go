package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// The preflight describes the target host, so a caller needs the same
// deploy right there as for the migration itself.
func TestPreflightNeedsDeployOnTarget(t *testing.T) {
	e := newScopeEnv(t)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	e.h.Stacks = mgr
	e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksMigrate)).Post("/api/v1/stacks/{name}/migrate/preflight", e.h.PreflightMigration)
	})
	if err := e.h.Deployments.Set(context.Background(), "web", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	e.role("local-mover", []rbac.Perm{rbac.PermStacksMigrate, rbac.PermStacksDeploy, rbac.PermStacksView}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	e.user("mover", "local-mover")
	tok := e.login("mover").AccessToken

	tests := []struct {
		name, target string
		forbidden    bool
		want         int
	}{
		{"target outside the caller's scope", "agent-2", true, http.StatusForbidden},
		{"no target", "", false, http.StatusBadRequest},
		{"target in scope", "local", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(http.MethodPost, "/api/v1/stacks/web/migrate/preflight", tok, map[string]any{"target_host_id": tt.target})
			if tt.want != 0 && rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			if !tt.forbidden && rr.Code == http.StatusForbidden {
				t.Fatalf("refused: %s", rr.Body)
			}
		})
	}
}

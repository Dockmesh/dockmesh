package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// A migration's routes act on the hosts in its record, not on wherever the
// stack runs now: a rollback deploys on the source, a purge cleans it up.
func TestMigrationRoutesCheckTheMigrationsHosts(t *testing.T) {
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
		r.Use(middleware.RequirePerm(rbac.PermStacksMigrate))
		r.Get("/api/v1/stacks/{name}/migrate/{id}", e.h.GetMigration)
		r.Post("/api/v1/stacks/{name}/migrate/{id}/rollback", e.h.RollbackMigration)
		r.Delete("/api/v1/stacks/{name}/migrate/{id}/source", e.h.PurgeSource)
	})
	ctx := context.Background()
	if err := e.h.Deployments.Set(ctx, "web", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Running: the service refuses rollback and purge, so a request that
	// gets past the scope checks ends in 400, never in a real action.
	for _, m := range []*migration.Migration{
		{ID: "moved-here", StackName: "web", SourceHostID: "agent-1", TargetHostID: "local", Status: migration.StatusHealthCheck, StartedAt: &now},
		{ID: "elsewhere", StackName: "web", SourceHostID: "agent-1", TargetHostID: "agent-2", Status: migration.StatusHealthCheck, StartedAt: &now},
	} {
		if err := e.h.Migrations.Store().Create(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	full := []rbac.Perm{rbac.PermStacksMigrate, rbac.PermStacksDeploy, rbac.PermStacksView}
	local := rbac.Scope{Type: rbac.ScopeHost, Host: "local"}
	agent1 := rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"}
	e.role("target-only", full, local)
	e.role("both", full, local, agent1)
	tok := map[string]string{}
	for _, u := range []string{"target-only", "both"} {
		e.user(u, u)
		tok[u] = e.login(u).AccessToken
	}

	tests := []struct {
		name, user, method, path string
		want                     int
	}{
		{"get with rights on the target", "target-only", http.MethodGet, "/api/v1/stacks/web/migrate/moved-here", http.StatusOK},
		{"get a migration between other hosts", "target-only", http.MethodGet, "/api/v1/stacks/web/migrate/elsewhere", http.StatusNotFound},
		{"rollback without rights on the source", "target-only", http.MethodPost, "/api/v1/stacks/web/migrate/moved-here/rollback", http.StatusForbidden},
		{"rollback with rights on both", "both", http.MethodPost, "/api/v1/stacks/web/migrate/moved-here/rollback", http.StatusBadRequest},
		{"rollback of a migration to another target", "both", http.MethodPost, "/api/v1/stacks/web/migrate/elsewhere/rollback", http.StatusForbidden},
		{"purge without rights on the source", "target-only", http.MethodDelete, "/api/v1/stacks/web/migrate/moved-here/source", http.StatusForbidden},
		{"purge with rights on both", "both", http.MethodDelete, "/api/v1/stacks/web/migrate/moved-here/source", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tok[tt.user], nil)
			if rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}

// The drain routes take the drain id from the URL; a drain of another
// host is not found under this one.
func TestDrainRoutesNeedTheirHost(t *testing.T) {
	e := newScopeEnv(t)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	migSvc := migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
	e.h.Drains = migration.NewDrainService(migSvc, e.db)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(middleware.RequirePerm(rbac.PermHostsUpdate))
		r.Get("/api/v1/hosts/{id}/drain/{drain_id}", e.h.GetDrain)
		r.Post("/api/v1/hosts/{id}/drain/{drain_id}/pause", e.h.PauseDrain)
		r.Post("/api/v1/hosts/{id}/drain/{drain_id}/resume", e.h.ResumeDrain)
		r.Post("/api/v1/hosts/{id}/drain/{drain_id}/abort", e.h.AbortDrain)
	})
	now := time.Now()
	if err := e.h.Drains.Store().Create(context.Background(), &migration.Drain{ID: "d1", SourceHostID: "agent-1", Status: migration.DrainExecuting, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	e.user("root", "admin")
	tok := e.login("root").AccessToken

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"get under its host", http.MethodGet, "/api/v1/hosts/agent-1/drain/d1", http.StatusOK},
		{"get under another host", http.MethodGet, "/api/v1/hosts/agent-2/drain/d1", http.StatusNotFound},
		{"pause under another host", http.MethodPost, "/api/v1/hosts/agent-2/drain/d1/pause", http.StatusNotFound},
		{"resume under another host", http.MethodPost, "/api/v1/hosts/agent-2/drain/d1/resume", http.StatusNotFound},
		{"abort under another host", http.MethodPost, "/api/v1/hosts/agent-2/drain/d1/abort", http.StatusNotFound},
		{"abort under its host, not running here", http.MethodPost, "/api/v1/hosts/agent-1/drain/d1/abort", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tok, nil)
			if rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}

// Hosts: Manage alone doesn't move stacks: each stack of the drain needs
// what a manual migration of it would.
func TestDrainMovesNeedStackRights(t *testing.T) {
	e := newScopeEnv(t)
	e.role("hosts-only", []rbac.Perm{rbac.PermHostsUpdate, rbac.PermStacksView}, rbac.Global)
	e.role("mover", []rbac.Perm{rbac.PermHostsUpdate, rbac.PermStacksMigrate, rbac.PermStacksDeploy}, rbac.Global)
	e.role("source-only", []rbac.Perm{rbac.PermHostsUpdate, rbac.PermStacksMigrate, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"})
	ids := map[string]string{}
	for _, u := range []string{"hosts-only", "mover", "source-only"} {
		ids[u] = e.user(u, u).ID
	}
	entries := []migration.PlanEntry{{StackName: "web", TargetHostID: "agent-2", Feasible: true}}

	tests := []struct {
		user string
		want bool
	}{
		{"hosts-only", false},
		{"mover", true},
		{"source-only", false},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/hosts/agent-1/drain/execute", nil)
			id := ids[tt.user]
			req = req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: id, Access: e.h.Roles.AccessFor(id)}))
			rr := httptest.NewRecorder()
			if got := e.h.checkDrainMoves(rr, req, "agent-1", entries); got != tt.want {
				t.Fatalf("got %v (%d %s), want %v", got, rr.Code, rr.Body, tt.want)
			}
			if !tt.want && rr.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rr.Code)
			}
		})
	}
}

package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestMigrationActiveHasItsCode(t *testing.T) {
	if c := errorCode(http.StatusConflict, migration.ErrMigrationActive.Error()); c != "migration_active" {
		t.Fatalf("code of ErrMigrationActive = %q", c)
	}
}

func TestDeployAndDeleteRefuseActiveMigration(t *testing.T) {
	tests := []struct {
		name       string
		status     string // "" = no migration
		method     string
		path       string
		wantStatus int
	}{
		{"deploy, no migration", "", http.MethodPost, "/stacks/app/deploy", http.StatusOK},
		{"deploy while migrating", migration.StatusSyncing, http.MethodPost, "/stacks/app/deploy", http.StatusConflict},
		{"deploy while rolling back", migration.StatusRollingBack, http.MethodPost, "/stacks/app/deploy", http.StatusConflict},
		{"deploy after a failed migration", migration.StatusFailed, http.MethodPost, "/stacks/app/deploy", http.StatusOK},
		{"delete while migrating", migration.StatusPending, http.MethodDelete, "/stacks/app", http.StatusConflict},
		{"delete while rolling back", migration.StatusRollingBack, http.MethodDelete, "/stacks/app", http.StatusConflict},
		{"delete after a completed migration", migration.StatusCompleted, http.MethodDelete, "/stacks/app", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newScopeEnv(t)
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Close() })
			mgr.SetSumStore(stacks.NewSumStore(e.db))
			local := &deployHost{recordingHost{id: "local"}}
			e.h.Stacks = mgr
			e.h.Hosts = host.NewFixedRegistry(local)
			e.h.Deployments = stacks.NewDeploymentStore(e.db)
			e.h.StackLocks = stacklock.New()
			e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
			e.router.Group(func(r chi.Router) {
				r.Use(middleware.NewAuth(e.h.Auth))
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/deploy", e.h.DeployStack)
				r.With(middleware.RequirePerm(rbac.PermStacksDelete)).Delete("/stacks/{name}", e.h.DeleteStack)
			})
			e.user("root", "admin")
			tok := e.login("root").AccessToken
			if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
				t.Fatal(err)
			}
			if err := e.h.Deployments.Set(context.Background(), "app", "local", "deployed"); err != nil {
				t.Fatal(err)
			}
			if tt.status != "" {
				now := time.Now()
				m := &migration.Migration{ID: "m1", StackName: "app", SourceHostID: "local", TargetHostID: "agent-1",
					Status: tt.status, StartedAt: &now, InitiatedBy: "u"}
				if err := e.h.Migrations.Store().Create(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}

			rr := e.do(tt.method, tt.path, tok, nil)
			if rr.Code != tt.wantStatus {
				t.Fatalf("%s %s: %d %s, want %d", tt.method, tt.path, rr.Code, rr.Body, tt.wantStatus)
			}
			refused := tt.wantStatus == http.StatusConflict
			if refused && !strings.Contains(rr.Body.String(), `"code":"migration_active"`) {
				t.Errorf("409 without migration_active: %s", rr.Body)
			}
			if refused && (len(local.took()) > 0 || !mgr.DirExists("app")) {
				t.Errorf("refused request still acted: calls %v, stack dir kept %v", local.took(), mgr.DirExists("app"))
			}
		})
	}
}

// Rolling back or finalising a migration while a later one of the same
// stack is unfinished answers 409 migration_active.
func TestMigrationEndpointsRefuseActiveMigration(t *testing.T) {
	tests := []struct{ name, method, path string }{
		{"rollback", http.MethodPost, "/stacks/app/migrate/m1/rollback"},
		{"purge source", http.MethodDelete, "/stacks/app/migrate/m1/source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newScopeEnv(t)
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Close() })
			e.h.Stacks = mgr
			e.h.Hosts = host.NewFixedRegistry(&deployHost{recordingHost{id: "local"}})
			e.h.Deployments = stacks.NewDeploymentStore(e.db)
			e.h.StackLocks = stacklock.New()
			e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
			e.h.Migrations.SetLocks(e.h.StackLocks)
			e.router.Group(func(r chi.Router) {
				r.Use(middleware.NewAuth(e.h.Auth))
				r.With(middleware.RequirePerm(rbac.PermStacksMigrate)).Post("/stacks/{name}/migrate/{id}/rollback", e.h.RollbackMigration)
				r.With(middleware.RequirePerm(rbac.PermStacksMigrate)).Delete("/stacks/{name}/migrate/{id}/source", e.h.PurgeSource)
			})
			e.user("root", "admin")
			tok := e.login("root").AccessToken
			if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			for _, m := range []*migration.Migration{
				{ID: "m1", StackName: "app", SourceHostID: "local", TargetHostID: "agent-1", Status: migration.StatusCompleted},
				{ID: "m2", StackName: "app", SourceHostID: "agent-1", TargetHostID: "local", Status: migration.StatusSyncing},
			} {
				m.StartedAt, m.InitiatedBy = &now, "u"
				if err := e.h.Migrations.Store().Create(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			rr := e.do(tt.method, tt.path, tok, nil)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"code":"migration_active"`) {
				t.Fatalf("%s: %d %s", tt.name, rr.Code, rr.Body)
			}
		})
	}
}

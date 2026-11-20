package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/compose"
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

// scaleHost fakes the calls of a scale and a rolling update.
type scaleHost struct{ deployHost }

func (f *scaleHost) CheckScale(context.Context, string, string, string, string) (*compose.ScaleCheck, error) {
	return &compose.ScaleCheck{}, nil
}

func (f *scaleHost) ScaleService(_ context.Context, name, _, _, service string, replicas int) (*compose.ScaleResult, error) {
	f.record("scale " + name + "/" + service)
	return &compose.ScaleResult{Current: replicas}, nil
}

func (f *scaleHost) RollingReplace(_ context.Context, name, _, _, service string, _ compose.RollingOptions) (*compose.RollingResult, error) {
	f.record("rolling " + name + "/" + service)
	return &compose.RollingResult{}, nil
}

func TestScaleAndRollingUpdateRefuseActiveMigration(t *testing.T) {
	tests := []struct {
		name       string
		status     string // "" = no migration
		path       string
		body       any
		wantStatus int
	}{
		{"scale, no migration", "", "/stacks/app/services/web/scale", map[string]int{"replicas": 1}, http.StatusOK},
		{"scale while migrating", migration.StatusSyncing, "/stacks/app/services/web/scale", map[string]int{"replicas": 1}, http.StatusConflict},
		{"scale while rolling back", migration.StatusRollingBack, "/stacks/app/services/web/scale", map[string]int{"replicas": 1}, http.StatusConflict},
		{"scale after a rolled back migration", migration.StatusRolledBack, "/stacks/app/services/web/scale", map[string]int{"replicas": 1}, http.StatusOK},
		{"rolling update, no migration", "", "/stacks/app/services/web/rolling-update", nil, http.StatusOK},
		{"rolling update while migrating", migration.StatusPending, "/stacks/app/services/web/rolling-update", nil, http.StatusConflict},
		{"rolling update while rolling back", migration.StatusRollingBack, "/stacks/app/services/web/rolling-update", nil, http.StatusConflict},
		{"rolling update after a completed migration", migration.StatusCompleted, "/stacks/app/services/web/rolling-update", nil, http.StatusOK},
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
			local := &scaleHost{deployHost{recordingHost{id: "local"}}}
			e.h.Stacks = mgr
			e.h.Hosts = host.NewFixedRegistry(local)
			e.h.Deployments = stacks.NewDeploymentStore(e.db)
			e.h.StackLocks = stacklock.New()
			e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
			e.router.Group(func(r chi.Router) {
				r.Use(middleware.NewAuth(e.h.Auth))
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/services/{service}/scale", e.h.ScaleService)
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/services/{service}/rolling-update", e.h.RollingUpdateService)
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

			rr := e.do(http.MethodPost, tt.path, tok, tt.body)
			if rr.Code != tt.wantStatus {
				t.Fatalf("POST %s: %d %s, want %d", tt.path, rr.Code, rr.Body, tt.wantStatus)
			}
			refused := tt.wantStatus == http.StatusConflict
			if refused && !strings.Contains(rr.Body.String(), `"code":"migration_active"`) {
				t.Errorf("409 without migration_active: %s", rr.Body)
			}
			if acted := len(local.took()) > 0; acted == refused {
				t.Errorf("refused = %v, host calls %v", refused, local.took())
			}
			release, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpDeploy)
			if err != nil {
				t.Fatalf("stack lock kept: %v", err)
			}
			release()
		})
	}
}

func TestImageUpdateMigrationActiveIs409(t *testing.T) {
	rr := httptest.NewRecorder()
	if !writeMigrationConflict(rr, fmt.Errorf("update: %w", migration.ErrMigrationActive)) {
		t.Fatal("ErrMigrationActive not answered")
	}
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"code":"migration_active"`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
}

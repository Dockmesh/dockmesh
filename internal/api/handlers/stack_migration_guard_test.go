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
	"github.com/moby/moby/api/types/container"

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

// writeOpsHost fakes a stack stop and the container actions; containers
// "app-web" and "other-web" belong to compose projects, "solo" to none.
type writeOpsHost struct{ deployHost }

func (f *writeOpsHost) InspectContainer(_ context.Context, id string) (container.InspectResponse, error) {
	labels := map[string]string{}
	switch id {
	case "app-web":
		labels[compose.LabelProject], labels[compose.LabelService] = "app", "web"
	case "other-web":
		labels[compose.LabelProject], labels[compose.LabelService] = "other", "web"
	}
	return container.InspectResponse{ID: id, Config: &container.Config{Labels: labels}}, nil
}

func (f *writeOpsHost) StopStack(_ context.Context, name string) error {
	f.record("stop " + name)
	return nil
}

func (f *writeOpsHost) StartContainer(_ context.Context, id string) error {
	f.record("start " + id)
	return nil
}

func (f *writeOpsHost) StopContainer(_ context.Context, id string) error {
	f.record("stop " + id)
	return nil
}

func (f *writeOpsHost) RestartContainer(_ context.Context, id string) error {
	f.record("restart " + id)
	return nil
}

func (f *writeOpsHost) PauseContainer(_ context.Context, id string) error {
	f.record("pause " + id)
	return nil
}

func (f *writeOpsHost) UnpauseContainer(_ context.Context, id string) error {
	f.record("unpause " + id)
	return nil
}

func (f *writeOpsHost) KillContainer(_ context.Context, id, _ string) error {
	f.record("kill " + id)
	return nil
}

func (f *writeOpsHost) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.record("remove " + id)
	return nil
}

func TestStackWriteOpsRefuseActiveMigration(t *testing.T) {
	type route struct {
		name, method, path string
		body               any
		ok                 int
		guarded            bool // false: not a managed stack, never refused
	}
	routes := []route{
		{"stack stop", http.MethodPost, "/stacks/app/stop", nil, http.StatusNoContent, true},
		{"file save", http.MethodPut, "/stacks/app", map[string]string{"compose": "services: {web: {image: y}}\n"}, http.StatusOK, true},
		{"active environment", http.MethodPut, "/stacks/app/environments/active", map[string]string{"active": "prod"}, http.StatusOK, true},
		{"container start", http.MethodPost, "/containers/app-web/start", nil, http.StatusNoContent, true},
		{"container stop", http.MethodPost, "/containers/app-web/stop", nil, http.StatusNoContent, true},
		{"container restart", http.MethodPost, "/containers/app-web/restart", nil, http.StatusNoContent, true},
		{"container pause", http.MethodPost, "/containers/app-web/pause", nil, http.StatusNoContent, true},
		{"container unpause", http.MethodPost, "/containers/app-web/unpause", nil, http.StatusNoContent, true},
		{"container kill", http.MethodPost, "/containers/app-web/kill", nil, http.StatusNoContent, true},
		{"container remove", http.MethodDelete, "/containers/app-web", nil, http.StatusNoContent, true},
		{"unmanaged project container stop", http.MethodPost, "/containers/other-web/stop", nil, http.StatusNoContent, false},
		{"standalone container remove", http.MethodDelete, "/containers/solo", nil, http.StatusNoContent, false},
	}
	states := []struct {
		name, status string // "" = no migration
		active       bool
	}{
		{"no migration", "", false},
		{"migrating", migration.StatusSyncing, true},
		{"rolling back", migration.StatusRollingBack, true},
		{"completed", migration.StatusCompleted, false},
		{"rolled back", migration.StatusRolledBack, false},
	}
	for _, rt := range routes {
		for _, st := range states {
			t.Run(rt.name+", "+st.name, func(t *testing.T) {
				e := newScopeEnv(t)
				mgr, err := stacks.NewManager(t.TempDir(), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = mgr.Close() })
				mgr.SetSumStore(stacks.NewSumStore(e.db))
				local := &writeOpsHost{deployHost{recordingHost{id: "local"}}}
				e.h.Stacks = mgr
				e.h.Hosts = host.NewFixedRegistry(local)
				e.h.Deployments = stacks.NewDeploymentStore(e.db)
				e.h.StackLocks = stacklock.New()
				e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
				e.router.Group(func(r chi.Router) {
					r.Use(middleware.NewAuth(e.h.Auth))
					r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/stop", e.h.StopStack)
					r.With(middleware.RequirePerm(rbac.PermStacksUpdate)).Put("/stacks/{name}", e.h.UpdateStack)
					r.With(middleware.RequirePerm(rbac.PermStacksUpdate)).Put("/stacks/{name}/environments/active", e.h.SetActiveEnvironment)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/start", e.h.StartContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/stop", e.h.StopContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/restart", e.h.RestartContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/pause", e.h.PauseContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/unpause", e.h.UnpauseContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/kill", e.h.KillContainer)
					r.With(middleware.RequirePerm(rbac.PermContainersDelete)).Delete("/containers/{id}", e.h.RemoveContainer)
				})
				e.user("root", "admin")
				tok := e.login("root").AccessToken
				const original = "services: {web: {image: x}}\n"
				if _, err := mgr.Create("app", original, ""); err != nil {
					t.Fatal(err)
				}
				if err := e.h.Deployments.Set(context.Background(), "app", "local", "deployed"); err != nil {
					t.Fatal(err)
				}
				if st.status != "" {
					now := time.Now()
					for _, stack := range []string{"app", "other"} {
						m := &migration.Migration{ID: "m-" + stack, StackName: stack, SourceHostID: "local", TargetHostID: "agent-1",
							Status: st.status, StartedAt: &now, InitiatedBy: "u"}
						if err := e.h.Migrations.Store().Create(context.Background(), m); err != nil {
							t.Fatal(err)
						}
					}
				}

				rr := e.do(rt.method, rt.path, tok, rt.body)
				refused := rt.guarded && st.active
				want := rt.ok
				if refused {
					want = http.StatusConflict
				}
				if rr.Code != want {
					t.Fatalf("%s %s: %d %s, want %d", rt.method, rt.path, rr.Code, rr.Body, want)
				}
				if refused && !strings.Contains(rr.Body.String(), `"code":"migration_active"`) {
					t.Errorf("409 without migration_active: %s", rr.Body)
				}
				if refused && len(local.took()) > 0 {
					t.Errorf("refused request still called the host: %v", local.took())
				}
				if refused {
					d, err := mgr.Get("app")
					if err != nil {
						t.Fatal(err)
					}
					if d.Compose != original {
						t.Errorf("refused save wrote compose.yaml: %q", d.Compose)
					}
				}
				release, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpDeploy)
				if err != nil {
					t.Fatalf("stack lock kept: %v", err)
				}
				release()
			})
		}
	}
}

// A stack another operation holds answers stack_busy.
func TestStackWriteOpsRefuseBusyStack(t *testing.T) {
	routes := []struct {
		name, method, path string
		body               any
	}{
		{"stack stop", http.MethodPost, "/stacks/app/stop", nil},
		{"file save", http.MethodPut, "/stacks/app", map[string]string{"compose": "services: {web: {image: y}}\n"}},
		{"active environment", http.MethodPut, "/stacks/app/environments/active", map[string]string{"active": "prod"}},
		{"container restart", http.MethodPost, "/containers/app-web/restart", nil},
		{"container remove", http.MethodDelete, "/containers/app-web", nil},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			e := newScopeEnv(t)
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Close() })
			local := &writeOpsHost{deployHost{recordingHost{id: "local"}}}
			e.h.Stacks = mgr
			e.h.Hosts = host.NewFixedRegistry(local)
			e.h.Deployments = stacks.NewDeploymentStore(e.db)
			e.h.StackLocks = stacklock.New()
			e.router.Group(func(r chi.Router) {
				r.Use(middleware.NewAuth(e.h.Auth))
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/stop", e.h.StopStack)
				r.With(middleware.RequirePerm(rbac.PermStacksUpdate)).Put("/stacks/{name}", e.h.UpdateStack)
				r.With(middleware.RequirePerm(rbac.PermStacksUpdate)).Put("/stacks/{name}/environments/active", e.h.SetActiveEnvironment)
				r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/restart", e.h.RestartContainer)
				r.With(middleware.RequirePerm(rbac.PermContainersDelete)).Delete("/containers/{id}", e.h.RemoveContainer)
			})
			e.user("root", "admin")
			tok := e.login("root").AccessToken
			if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
				t.Fatal(err)
			}
			if err := e.h.Deployments.Set(context.Background(), "app", "local", "deployed"); err != nil {
				t.Fatal(err)
			}
			release, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpMigration)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			rr := e.do(rt.method, rt.path, tok, rt.body)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"code":"stack_busy"`) {
				t.Fatalf("%s: %d %s", rt.name, rr.Code, rr.Body)
			}
			if len(local.took()) > 0 {
				t.Errorf("busy stack still called the host: %v", local.took())
			}
		})
	}
}

// A container on a host the stack neither lives on nor migrates from or
// to is not refused while that stack migrates elsewhere.
func TestContainerActionIgnoresMigrationOnOtherHosts(t *testing.T) {
	e := newScopeEnv(t)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	local := &writeOpsHost{deployHost{recordingHost{id: "local"}}}
	e.h.Stacks = mgr
	e.h.Hosts = host.NewFixedRegistry(local)
	e.h.Deployments = stacks.NewDeploymentStore(e.db)
	e.h.StackLocks = stacklock.New()
	e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermContainersUpdate)).Post("/containers/{id}/restart", e.h.RestartContainer)
	})
	e.user("root", "admin")
	tok := e.login("root").AccessToken
	if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.h.Deployments.Set(ctx, "app", "agent-2", "deployed"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m := &migration.Migration{ID: "m1", StackName: "app", SourceHostID: "agent-2", TargetHostID: "agent-3",
		Status: migration.StatusRollingBack, StartedAt: &now, InitiatedBy: "u"}
	if err := e.h.Migrations.Store().Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodPost, "/containers/app-web/restart", tok, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("restart on an uninvolved host: %d %s", rr.Code, rr.Body)
	}
	m2 := &migration.Migration{ID: "m2", StackName: "app", SourceHostID: "agent-2", TargetHostID: "local",
		Status: migration.StatusSyncing, StartedAt: &now, InitiatedBy: "u"}
	if err := e.h.Migrations.Store().Create(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodPost, "/containers/app-web/restart", tok, nil); rr.Code != http.StatusConflict {
		t.Fatalf("restart on the migration's target host: %d %s", rr.Code, rr.Body)
	}
}

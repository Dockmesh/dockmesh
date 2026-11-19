package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type deployHost struct{ recordingHost }

func (f *deployHost) DeployStack(_ context.Context, name, _, _ string) (*compose.DeployResult, error) {
	f.record("deploy " + name)
	return &compose.DeployResult{Stack: name}, nil
}

// A confirmed request that is refused afterwards leaves the outside
// changes unreviewed; only a request that goes on to deploy records them.
func TestConfirmationRecordedOnlyBeforeTheWrite(t *testing.T) {
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
	e.h.StackLocks = stacklock.New()
	e.h.Audit = audit.NewService(e.db, "")
	e.h.Migrations = migration.NewService(e.db, e.h.Hosts, mgr, e.h.Deployments)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/deploy", e.h.DeployStack)
		r.With(middleware.RequirePerm(rbac.PermStacksMigrate)).Post("/stacks/{name}/migrate", e.h.InitiateMigration)
	})
	e.user("root", "admin")
	tok := e.login("root").AccessToken
	if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Deployments.Set(context.Background(), "app", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	dir, err := mgr.Dir("app")
	if err != nil {
		t.Fatal(err)
	}
	writeOutside(t, dir, "compose.yaml", "services: {web: {image: y}}\n")

	unreviewed := func(step string, want bool) {
		t.Helper()
		changed, err := mgr.ModifiedOutside("app")
		if err != nil {
			t.Fatal(err)
		}
		if got := len(changed) > 0; got != want {
			t.Fatalf("%s: changed outside = %v, want %v", step, changed, want)
		}
	}

	refused := []struct {
		name, method, path string
		body               any
		status             int
	}{
		{"deploy with a missing override", http.MethodPost, "/stacks/app/deploy?confirm_external=1&environment=nope", nil, http.StatusUnprocessableEntity},
		{"migrate to its own host", http.MethodPost, "/stacks/app/migrate?confirm_external=1", map[string]any{"target_host_id": "local"}, http.StatusBadRequest},
	}
	for _, tc := range refused {
		rr := e.do(tc.method, tc.path, tok, tc.body)
		if rr.Code != tc.status {
			t.Fatalf("%s: status %d, want %d: %s", tc.name, rr.Code, tc.status, rr.Body)
		}
		unreviewed(tc.name, true)
	}
	if calls := local.took(); len(calls) != 0 {
		t.Fatalf("a refused request wrote: %v", calls)
	}

	rr := e.do(http.MethodPost, "/stacks/app/deploy?confirm_external=1", tok, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirmed deploy: status %d: %s", rr.Code, rr.Body)
	}
	unreviewed("confirmed deploy", false)
	if calls := local.took(); len(calls) != 1 || calls[0] != "deploy app" {
		t.Fatalf("calls = %v", calls)
	}
}

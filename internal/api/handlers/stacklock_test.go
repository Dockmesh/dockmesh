package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestStackActionsRefuseBusyStack(t *testing.T) {
	routes := []struct {
		name, path string
		body       map[string]any
	}{
		{"deploy", "/stacks/app/deploy", nil},
		{"rolling update", "/stacks/app/services/web/rolling-update", map[string]any{"parallelism": 1}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			e := newScopeEnv(t)
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mgr.Close() })
			mgr.SetSumStore(stacks.NewSumStore(e.db))
			e.h.Stacks = mgr
			e.h.Hosts = host.NewRegistry(nil, nil)
			e.h.Audit = audit.NewService(e.db, "")
			e.h.StackLocks = stacklock.New()
			e.router.Group(func(r chi.Router) {
				r.Use(middleware.NewAuth(e.h.Auth))
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/deploy", e.h.DeployStack)
				r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/services/{service}/rolling-update", e.h.RollingUpdateService)
			})
			e.user("root", "admin")
			tok := e.login("root").AccessToken
			if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
				t.Fatal(err)
			}

			release, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpImageUpdate)
			if err != nil {
				t.Fatal(err)
			}
			rr := e.do(http.MethodPost, rt.path, tok, rt.body)
			if rr.Code != http.StatusConflict {
				t.Fatalf("%d, want 409: %s", rr.Code, rr.Body)
			}
			var body stackBusyResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "stack_busy" || body.Operation != stacklock.OpImageUpdate || time.Since(body.Since) > time.Minute {
				t.Fatalf("body = %+v", body)
			}

			release()
			rr = e.do(http.MethodPost, rt.path, tok, rt.body)
			if rr.Code == http.StatusConflict {
				t.Fatalf("after release: %d %s", rr.Code, rr.Body)
			}
			again, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpImageUpdate)
			if err != nil {
				t.Fatalf("the failed %s kept the lock: %v", rt.name, err)
			}
			again()
		})
	}
}

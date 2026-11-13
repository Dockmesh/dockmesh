package handlers

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func newDeleteLockEnv(t *testing.T) (*authzEnv, *stacks.Manager, string) {
	t.Helper()
	e := newScopeEnv(t)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	e.h.Stacks = mgr
	e.h.Deployments = stacks.NewDeploymentStore(e.db)
	e.h.StackLocks = stacklock.New()
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksDelete)).Delete("/stacks/{name}", e.h.DeleteStack)
	})
	e.user("root", "admin")
	if _, err := mgr.Create("web", "services:\n  a:\n    image: busybox\n", ""); err != nil {
		t.Fatal(err)
	}
	return e, mgr, e.login("root").AccessToken
}

func TestDeleteBusyStackConflicts(t *testing.T) {
	tests := []struct {
		name    string
		hostID  string // host the stack is deployed on; "" = not deployed
		lockKey stacklock.Key
	}{
		{"deploy on the local host", "", stacklock.HostKey("local", "web")},
		{"deploy on the deployment host", "agent-1", stacklock.HostKey("agent-1", "web")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, mgr, tok := newDeleteLockEnv(t)
			if tt.hostID != "" {
				if err := e.h.Deployments.Set(t.Context(), "web", tt.hostID, "deployed"); err != nil {
					t.Fatal(err)
				}
			}
			release, err := e.h.StackLocks.TryLock(tt.lockKey, stacklock.OpDeploy)
			if err != nil {
				t.Fatal(err)
			}
			rr := e.do(http.MethodDelete, "/stacks/web", tok, nil)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"operation":"deploy"`) {
				t.Fatalf("delete during a deploy: %d %s", rr.Code, rr.Body)
			}
			if !mgr.DirExists("web") {
				t.Fatal("a busy stack was deleted")
			}
			release()
			if rr := e.do(http.MethodDelete, "/stacks/web", tok, nil); rr.Code != http.StatusNoContent {
				t.Fatalf("delete after the deploy: %d %s", rr.Code, rr.Body)
			}
		})
	}
}

func TestConcurrentDeletesOneWins(t *testing.T) {
	e, mgr, tok := newDeleteLockEnv(t)
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.do(http.MethodDelete, "/stacks/web", tok, nil).Code
		}()
	}
	wg.Wait()
	sort.Ints(codes)
	if codes[0] != http.StatusNoContent || (codes[1] != http.StatusNotFound && codes[1] != http.StatusConflict) {
		t.Fatalf("codes = %v, want one 204 and one 404 or 409", codes)
	}
	if mgr.DirExists("web") {
		t.Error("stack kept")
	}
}

// Only the delete itself takes the stack lock; the dialog's preview and a
// recover are not refused while a deploy runs.
func TestOnlyDeleteTakesTheDeleteLock(t *testing.T) {
	e, _, tok := newDeleteLockEnv(t)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Get("/stacks/{name}/cleanup-preview", e.h.CleanupPreview)
		r.Post("/stacks/{name}/recover", e.h.RecoverStack)
	})
	release, err := e.h.StackLocks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/stacks/web/cleanup-preview"},
		{http.MethodPost, "/stacks/web/recover"},
	} {
		if rr := e.do(req.method, req.path, tok, nil); rr.Code == http.StatusConflict {
			t.Errorf("%s %s refused while a deploy runs: %s", req.method, req.path, rr.Body)
		}
	}
}

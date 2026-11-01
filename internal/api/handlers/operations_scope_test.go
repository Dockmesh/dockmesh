package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Operations follow the scope of what they work on: a caller only sees
// updates of containers and backups on hosts (and stacks) in scope, and
// system backups only as an unscoped admin.
func TestOperationsFollowScope(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-prod", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Operations = operations.New(e.db)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Get("/operations", e.h.ListOperations)
		r.Get("/operations/{id}", e.h.GetOperation)
		r.Post("/operations/{id}/cancel", e.h.CancelOperation)
	})

	start := func(spec operations.Spec) *operations.Op { return e.h.Operations.Start(ctx, spec) }
	start(operations.Spec{Kind: opImageUpdate, Target: "prod-web", HostID: "agent-prod", Stack: "web"})
	start(operations.Spec{Kind: opImageUpdate, Target: "local-db", HostID: "local", Stack: "db"})
	start(operations.Spec{Kind: opBackupRun, Target: "prod-nightly", HostID: "agent-prod"})
	start(operations.Spec{Kind: opBackupRun, Target: "system", HostID: "local", AdminOnly: true})
	start(operations.Spec{Kind: opProxySite, Target: "app.example.com"})
	queued := start(operations.Spec{Kind: opImageUpdate, Target: "local-web", HostID: "local", Stack: "web", Queued: true})
	queued.SetCancel(func() bool { return true })

	e.role("web-team", []rbac.Perm{rbac.PermContainersView, rbac.PermContainersUpdate, rbac.PermBackupsView}, rbac.Scope{Type: "stack", Value: "web"})
	e.role("proxy-only", []rbac.Perm{rbac.PermProxyView})
	e.user("boss", "admin")
	scoped := e.user("prodadmin", "admin")
	e.scopeUser(scoped.ID, "prod")
	e.user("web", "web-team")
	e.user("px", "proxy-only")

	targets := func(user string) []string {
		rr := e.do(http.MethodGet, "/operations", e.login(user).AccessToken, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", user, rr.Code, rr.Body)
		}
		var got []operationView
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, op := range got {
			out = append(out, op.Target)
		}
		sort.Strings(out)
		return out
	}
	tests := []struct {
		user string
		want string
	}{
		{"boss", "app.example.com,local-db,local-web,prod-nightly,prod-web,system"},
		{"prodadmin", "prod-nightly,prod-web"},
		{"web", "local-web,prod-web"},
		{"px", "app.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			if got := strings.Join(targets(tt.user), ","); got != tt.want {
				t.Fatalf("sees %s, want %s", got, tt.want)
			}
		})
	}

	if rr := e.do(http.MethodGet, "/operations/"+queued.ID(), e.login("prodadmin").AccessToken, nil); rr.Code != http.StatusNotFound {
		t.Errorf("out-of-scope operation: %d, want 404", rr.Code)
	}
	if rr := e.do(http.MethodPost, "/operations/"+queued.ID()+"/cancel", e.login("web").AccessToken, nil); rr.Code != http.StatusForbidden {
		t.Errorf("cancel of a stack update without stacks.deploy: %d, want 403", rr.Code)
	}
	if rr := e.do(http.MethodPost, "/operations/"+queued.ID()+"/cancel", e.login("boss").AccessToken, nil); rr.Code != http.StatusOK {
		t.Fatalf("cancel by admin: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodPost, "/operations/"+queued.ID()+"/cancel", e.login("boss").AccessToken, nil); rr.Code != http.StatusConflict {
		t.Errorf("second cancel: %d, want 409", rr.Code)
	}
}

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Hook commands are shown for confirmation, so a drain planner needs the
// right to read them on each stack that has any.
func TestDrainHooksNeedReadAccess(t *testing.T) {
	e := newScopeEnv(t)
	e.role("hosts-only", []rbac.Perm{rbac.PermHostsUpdate, rbac.PermStacksView}, rbac.Scope{Type: rbac.ScopeGlobal})
	e.role("deployer-here", []rbac.Perm{rbac.PermHostsUpdate, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeGlobal})
	ids := map[string]string{"hosts": e.user("hosts", "hosts-only").ID, "dep": e.user("dep", "deployer-here").ID}
	hooked := []migration.PlanEntry{{StackName: "db", Hooks: &migration.HookCommands{PreDump: "pg_dump", Digest: "d"}}, {StackName: "web"}}
	plain := []migration.PlanEntry{{StackName: "web"}}

	tests := []struct {
		name, user string
		entries    []migration.PlanEntry
		want       bool
	}{
		{"no hooks", "hosts", plain, true},
		{"hooks without read access", "hosts", hooked, false},
		{"hooks with deploy", "dep", hooked, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/hosts/local/drain/plan", nil)
			id := ids[tt.user]
			req = req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: id, Access: e.h.Roles.AccessFor(id)}))
			rr := httptest.NewRecorder()
			if got := e.h.checkDrainHooksReadable(rr, req, "local", tt.entries); got != tt.want {
				t.Fatalf("got %v (%d %s), want %v", got, rr.Code, rr.Body, tt.want)
			}
			if !tt.want && rr.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rr.Code)
			}
		})
	}
}

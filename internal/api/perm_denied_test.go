package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A missing permission answers the API's JSON error with code forbidden
// and leaves an auth.scope_denied entry, like a refused scope does.
func TestPermissionDenialIsJSONAndAudited(t *testing.T) {
	var auditSvc *audit.Service
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		auditSvc = audit.NewService(db, t.TempDir()+"/genesis")
		d.Audit = auditSvc
	})
	ctx := context.Background()
	if err := auditSvc.EnsureGenesis(ctx); err != nil {
		t.Fatal(err)
	}
	token := signIn(t, authSvc, roles, "dep", rbac.RoleDeployer, rbac.Global)
	router := NewRouter(h, authSvc, nil, true, nil)

	longID := strings.Repeat("x", 2000)
	tests := []struct {
		name, method, path string
		perm               rbac.Perm
		target             string
	}{
		{"users", http.MethodGet, "/api/v1/users", rbac.PermUsersView, "GET /api/v1/users"},
		{"audit", http.MethodGet, "/api/v1/audit", rbac.PermAuditView, "GET /api/v1/audit"},
		{"key export", http.MethodGet, "/api/v1/system/backup-key/export", rbac.PermBackupsKeyExport, "GET /api/v1/system/backup-key/export"},
		{"long path cut", http.MethodGet, "/api/v1/users/" + longID, rbac.PermUsersView, "GET " + ("/api/v1/users/" + longID)[:512] + "..."},
		{"control characters escaped", http.MethodGet, "/api/v1/users/a%0Ab", rbac.PermUsersView, "GET /api/v1/users/a%0Ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rr.Code)
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type %q, want application/json", ct)
			}
			var body struct{ Error, Code string }
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rr.Body.String(), err)
			}
			if body.Code != "forbidden" {
				t.Errorf("code %q, want forbidden", body.Code)
			}

			entries, err := auditSvc.ListFiltered(ctx, audit.ListFilter{Action: audit.ActionScopeDenied, Limit: 1000})
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range entries {
				var d map[string]any
				if err := json.Unmarshal([]byte(e.Details), &d); err != nil {
					t.Fatal(err)
				}
				if e.Target == tt.target && d["permission"] == string(tt.perm) && e.Username == "dep" {
					found = true
				}
			}
			if !found {
				t.Errorf("no auth.scope_denied entry for %s by dep in %+v", tt.perm, entries)
			}
		})
	}
}

// A caller looping a refused request leaves DenialBurst entries per
// window, not one per request, and the chain stays intact.
func TestPermissionDenialBurstIsLimited(t *testing.T) {
	var auditSvc *audit.Service
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		auditSvc = audit.NewService(db, t.TempDir()+"/genesis")
		d.Audit = auditSvc
	})
	ctx := context.Background()
	if err := auditSvc.EnsureGenesis(ctx); err != nil {
		t.Fatal(err)
	}
	token := signIn(t, authSvc, roles, "viewer", rbac.RoleViewer, rbac.Global)
	router := NewRouter(h, authSvc, nil, true, nil)

	for i := 0; i < 10000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("request %d: status %d, want 403", i, rr.Code)
		}
	}
	entries, err := auditSvc.ListFiltered(ctx, audit.ListFilter{Action: audit.ActionScopeDenied, Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > audit.DenialBurst+1 {
		t.Fatalf("%d auth.scope_denied entries for 10000 refusals, want at most %d", len(entries), audit.DenialBurst+1)
	}
	rep, err := auditSvc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Broken != 0 {
		t.Fatalf("chain broken: %+v", rep)
	}
}

// Refusals decided in a handler, after the route's permission passed,
// leave the same auth.scope_denied entry as a refused route.
func TestHandlerDenialIsAudited(t *testing.T) {
	var auditSvc *audit.Service
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		auditSvc = audit.NewService(db, t.TempDir()+"/genesis")
		d.Audit = auditSvc
	})
	ctx := context.Background()
	if err := auditSvc.EnsureGenesis(ctx); err != nil {
		t.Fatal(err)
	}
	hostAdmin := signIn(t, authSvc, roles, "hostadmin", rbac.RoleHostAdmin, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	router := NewRouter(h, authSvc, nil, true, nil)

	tests := []struct {
		name, method, path, body string
		perm                     rbac.Perm
	}{
		{"agent upgrade policy", http.MethodPut, "/api/v1/agents/upgrade-policy", `{"mode":"auto"}`, rbac.PermHostsUpdate},
		{"drain", http.MethodPost, "/api/v1/hosts/local/drain/plan", `{}`, rbac.PermHostsUpdate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+hostAdmin)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rr.Code, rr.Body.String())
			}
			entries, err := auditSvc.ListFiltered(ctx, audit.ListFilter{Action: audit.ActionScopeDenied, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			want := tt.method + " " + tt.path
			for _, e := range entries {
				var d map[string]any
				if err := json.Unmarshal([]byte(e.Details), &d); err != nil {
					t.Fatal(err)
				}
				if e.Target == want && d["permission"] == string(tt.perm) && e.Username == "hostadmin" {
					return
				}
			}
			t.Errorf("no auth.scope_denied entry %q for %s in %+v", want, tt.perm, entries)
		})
	}
}

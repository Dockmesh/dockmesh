package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	tests := []struct {
		method, path string
		perm         rbac.Perm
	}{
		{http.MethodGet, "/api/v1/users", rbac.PermUsersView},
		{http.MethodGet, "/api/v1/audit", rbac.PermAuditView},
		{http.MethodGet, "/api/v1/system/backup-key/export", rbac.PermBackupsKeyExport},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
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
				if e.Target == tt.method+" "+tt.path && d["permission"] == string(tt.perm) && e.Username == "dep" {
					found = true
				}
			}
			if !found {
				t.Errorf("no auth.scope_denied entry for %s by dep in %+v", tt.perm, entries)
			}
		})
	}
}

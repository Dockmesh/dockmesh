package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// The archive directory ends up in the server unit's ReadWritePaths, so
// both endpoints that set it need system.upgrade and refuse a value the
// unit writer would refuse.
func TestAuditRetentionLocalDir(t *testing.T) {
	var store *settings.Store
	dataDir := filepath.Join(t.TempDir(), "data")
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		store = settings.NewStore(db)
		auditSvc := audit.NewService(db, t.TempDir()+"/genesis")
		d.Settings = store
		d.Audit = auditSvc
		d.AuditRetention = audit.NewRetention(db, auditSvc, store, nil, dataDir)
	})
	ctx := context.Background()
	adminToken := signIn(t, authSvc, roles, "u-admin", rbac.RoleAdmin, rbac.Global)
	router := NewRouter(h, authSvc, nil, true, nil)
	const restrictedID = "u-restricted"
	noUpgrade := roles.AccessForAssignments([]rbac.Assignment{{UserID: restrictedID, Role: rbac.RoleAdmin, Scope: rbac.Global}}).Restrict(map[rbac.Perm]bool{
		rbac.PermAuditView: true, rbac.PermAuditWrite: true, rbac.PermSystemUpdate: true,
	}, rbac.Global)

	retention := func(dir string) string {
		return `{"mode":"archive_local","days":30,"local_dir":"` + dir + `"}`
	}
	setting := func(dir string) string {
		return `[{"key":"audit.retention_local_dir","value":"` + dir + `"}]`
	}
	tests := []struct {
		name, method, path string
		body               func(string) string
		value, start       string
		restricted         bool
		wantCode           int
	}{
		{"retention: admin sets a directory", http.MethodPut, "/api/v1/audit/retention", retention, "/srv/audit-archive", "", false, http.StatusOK},
		{"retention: relative path", http.MethodPut, "/api/v1/audit/retention", retention, "audit-archive", "", false, http.StatusBadRequest},
		{"retention: quote", http.MethodPut, "/api/v1/audit/retention", retention, `/srv/a\"b`, "", false, http.StatusBadRequest},
		{"retention: control character", http.MethodPut, "/api/v1/audit/retention", retention, `/srv/a\nb`, "", false, http.StatusBadRequest},
		{"retention: system directory", http.MethodPut, "/api/v1/audit/retention", retention, "/etc/dockmesh", "", false, http.StatusBadRequest},
		{"retention: contains the data dir", http.MethodPut, "/api/v1/audit/retention", retention, filepath.Dir(dataDir), "", false, http.StatusBadRequest},
		{"retention: empty means the default", http.MethodPut, "/api/v1/audit/retention", retention, "", "/srv/old", false, http.StatusOK},
		{"retention: change without system.upgrade", http.MethodPut, "/api/v1/audit/retention", retention, "/srv/audit-archive", "", true, http.StatusForbidden},
		{"retention: unchanged without system.upgrade", http.MethodPut, "/api/v1/audit/retention", retention, "/srv/same", "/srv/same", true, http.StatusOK},
		{"retention: default folder without system.upgrade", http.MethodPut, "/api/v1/audit/retention", retention, filepath.Join(dataDir, "audit-archive"), "", true, http.StatusOK},
		{"retention: clearing without system.upgrade", http.MethodPut, "/api/v1/audit/retention", retention, "", "/srv/old", true, http.StatusOK},
		{"settings: clearing without system.upgrade", http.MethodPut, "/api/v1/settings", setting, "", "/srv/old", true, http.StatusOK},
		{"settings: admin sets a directory", http.MethodPut, "/api/v1/settings", setting, "/srv/audit-archive", "", false, http.StatusOK},
		{"settings: relative path", http.MethodPut, "/api/v1/settings", setting, "audit-archive", "", false, http.StatusBadRequest},
		{"settings: system directory", http.MethodPut, "/api/v1/settings", setting, "/usr/local/audit", "", false, http.StatusBadRequest},
		{"settings: change without system.upgrade", http.MethodPut, "/api/v1/settings", setting, "/srv/audit-archive", "", true, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(ctx, audit.RetentionLocalDir, tt.start); err != nil {
				t.Fatal(err)
			}
			reqCtx := ctx
			if tt.restricted {
				reqCtx = middleware.WithIdentity(ctx, middleware.Identity{UserID: restrictedID, Access: noUpgrade})
			}
			req := httptest.NewRequestWithContext(reqCtx, tt.method, tt.path, strings.NewReader(tt.body(tt.value)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			if tt.restricted {
				if tt.path == "/api/v1/settings" {
					h.UpdateSettings(rr, req)
				} else {
					h.UpdateAuditRetention(rr, req)
				}
			} else {
				req.Header.Set("Authorization", "Bearer "+adminToken)
				router.ServeHTTP(rr, req)
			}
			if rr.Code != tt.wantCode {
				t.Fatalf("status %d %s, want %d", rr.Code, rr.Body, tt.wantCode)
			}
			want := tt.start
			if rr.Code == http.StatusOK {
				want = strings.ReplaceAll(tt.value, `\"`, `"`)
			}
			if got := store.Get(audit.RetentionLocalDir, ""); got != want {
				t.Errorf("stored %q, want %q", got, want)
			}
		})
	}
}

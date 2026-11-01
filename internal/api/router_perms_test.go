package api

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Route-level gates of the real router for the sensitive downloads,
// the host-admin change and the proxy version and settings changes: a
// 403 means the permission middleware refused, anything else means the
// request reached the handler.
func TestSensitiveRoutePermissions(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authSvc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
	roles := rbac.NewStore(database)
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}
	prev := middleware.RBACStore
	middleware.RBACStore = roles
	t.Cleanup(func() { middleware.RBACStore = prev })

	h := handlers.New(handlers.Deps{Auth: authSvc, Roles: roles})
	router := NewRouter(h, authSvc, fs.FS(nil), false, nil)

	tokens := map[string]string{}
	for _, role := range []string{"admin", "host-admin", "operator", "viewer"} {
		if _, err := authSvc.CreateUser(ctx, role+"-user", "", "correct horse battery", role); err != nil {
			t.Fatal(err)
		}
		res, err := authSvc.Login(ctx, role+"-user", "correct horse battery", "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = res.AccessToken
	}
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		role, method, path string
		denied             bool
	}{
		{"viewer", http.MethodGet, "/api/v1/backups/runs/1/archive", true},
		{"operator", http.MethodGet, "/api/v1/backups/runs/1/archive", true},
		{"host-admin", http.MethodGet, "/api/v1/backups/runs/1/archive", false},
		{"admin", http.MethodGet, "/api/v1/backups/runs/1/archive", false},
		{"viewer", http.MethodGet, "/api/v1/backups/runs/1", false},

		{"viewer", http.MethodGet, "/api/v1/containers/c1/files", true},
		{"viewer", http.MethodGet, "/api/v1/containers/c1/files/content?path=/etc/passwd", true},
		{"viewer", http.MethodGet, "/api/v1/containers/c1/files/download?path=/etc/passwd", true},
		{"operator", http.MethodGet, "/api/v1/containers/c1/files", true},
		{"host-admin", http.MethodGet, "/api/v1/containers/c1/files", false},

		{"host-admin", http.MethodGet, "/api/v1/settings", true},
		{"host-admin", http.MethodGet, "/api/v1/global-env", true},
		{"host-admin", http.MethodGet, "/api/v1/auth/providers", true},
		{"admin", http.MethodGet, "/api/v1/settings", false},

		{"viewer", http.MethodGet, "/api/v1/proxy/version", false},
		{"operator", http.MethodGet, "/api/v1/proxy/version", true},
		{"viewer", http.MethodGet, "/api/v1/proxy/settings", false},
		{"viewer", http.MethodPut, "/api/v1/proxy/version", true},
		{"viewer", http.MethodPost, "/api/v1/proxy/version/rollback", true},
		{"viewer", http.MethodPut, "/api/v1/proxy/settings", true},
		{"operator", http.MethodPut, "/api/v1/proxy/version", true},
		{"operator", http.MethodPost, "/api/v1/proxy/version/rollback", true},
		{"operator", http.MethodPut, "/api/v1/proxy/settings", true},
		{"host-admin", http.MethodPut, "/api/v1/proxy/version", false},
		{"host-admin", http.MethodPost, "/api/v1/proxy/version/rollback", false},
		{"host-admin", http.MethodPut, "/api/v1/proxy/settings", false},
	}
	for _, tt := range tests {
		t.Run(tt.role+" "+tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tokens[tt.role])
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if got := rr.Code == http.StatusForbidden; got != tt.denied {
				t.Errorf("status %d, denied=%v want %v: %s", rr.Code, got, tt.denied, rr.Body)
			}
		})
	}

	// The backup key is an extra only the Admin role has.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/backup-key/export", nil)
	req.Header.Set("Authorization", "Bearer "+tokens["host-admin"])
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("host-admin key export: %d, want 403", rr.Code)
	}
}

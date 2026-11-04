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
	"github.com/dockmesh/dockmesh/internal/settings"
)

// The base URL feeds invite links and sign-in callbacks, so changing it
// takes users.create on top of the system.update gate of PUT /settings.
func TestBaseURLChangeNeedsUsersCreate(t *testing.T) {
	const oldURL = "https://dockmesh.example.com"
	var store *settings.Store
	var auditSvc *audit.Service
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		store = settings.NewStore(db)
		auditSvc = audit.NewService(db, t.TempDir()+"/genesis")
		d.Settings = store
		d.Audit = auditSvc
	})
	ctx := context.Background()
	if err := auditSvc.EnsureGenesis(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range []rbac.RoleInput{
		{Name: "settings-only", Display: "Settings only", Levels: rbac.Levels{"settings": "manage"}},
		{Name: "settings-users", Display: "Settings and users", Levels: rbac.Levels{"settings": "manage", "users": "manage"}},
	} {
		if err := roles.CreateRole(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	for _, role := range []string{"settings-only", "settings-users", rbac.RoleAdmin} {
		tokens[role] = signIn(t, authSvc, roles, "u-"+role, role, rbac.Global)
	}
	router := NewRouter(h, authSvc, nil, true, nil)

	tests := []struct {
		name, role string
		body       string
		wantCode   int
		wantURL    string
	}{
		{"system.update alone cannot change it", "settings-only", `[{"key":"base_url","value":"https://evil.example"}]`, http.StatusForbidden, oldURL},
		{"system.update alone cannot clear it", "settings-only", `[{"key":"base_url","value":""}]`, http.StatusForbidden, oldURL},
		{"system.update alone may send it back unchanged", "settings-only", `[{"key":"base_url","value":"` + oldURL + `"},{"key":"scanner_enabled","value":"false"}]`, http.StatusOK, oldURL},
		{"system.update with users.create changes it", "settings-users", `[{"key":"base_url","value":"https://new.example"}]`, http.StatusOK, "https://new.example"},
		{"admin changes it", rbac.RoleAdmin, `[{"key":"base_url","value":"https://admin.example"}]`, http.StatusOK, "https://admin.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(ctx, settings.KeyBaseURL, oldURL); err != nil {
				t.Fatal(err)
			}
			before, err := auditSvc.List(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokens[tt.role])
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("status %d %s, want %d", rr.Code, rr.Body, tt.wantCode)
			}
			if got := store.Get(settings.KeyBaseURL, ""); got != tt.wantURL {
				t.Errorf("base_url %q, want %q", got, tt.wantURL)
			}
			after, err := auditSvc.List(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			var entries []audit.Entry
			for _, e := range after[:len(after)-len(before)] {
				if e.Action == "settings.base_url_update" {
					entries = append(entries, e)
				}
			}
			if tt.wantURL == oldURL {
				if len(entries) != 0 {
					t.Errorf("audit entries %v for an unchanged base URL", entries)
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("%d base URL audit entries, want 1", len(entries))
			}
			var details struct{ Before, After string }
			if err := json.Unmarshal([]byte(entries[0].Details), &details); err != nil {
				t.Fatal(err)
			}
			if details.Before != oldURL || details.After != tt.wantURL {
				t.Errorf("audit details %s, want before %q after %q", entries[0].Details, oldURL, tt.wantURL)
			}
		})
	}
}

package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// Install commands are built from the agent URL, so changing it takes
// hosts.create on top of the system.update gate of PUT /settings.
func TestAgentURLChangeNeedsHostsCreate(t *testing.T) {
	const oldURL = "wss://agents.example.com:8443/connect"
	var store *settings.Store
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		store = settings.NewStore(db)
		d.Settings = store
	})
	ctx := context.Background()
	for _, r := range []rbac.RoleInput{
		{Name: "settings-only", Display: "Settings only", Levels: rbac.Levels{"settings": "manage"}},
		{Name: "settings-hosts", Display: "Settings and hosts", Levels: rbac.Levels{"settings": "manage", "hosts": "manage"}},
	} {
		if err := roles.CreateRole(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	for _, role := range []string{"settings-only", "settings-hosts", rbac.RoleAdmin} {
		tokens[role] = signIn(t, authSvc, roles, "u-"+role, role, rbac.Global)
	}
	router := NewRouter(h, authSvc, nil, true, nil)

	tests := []struct {
		name, role string
		body       string
		wantCode   int
		wantURL    string
	}{
		{"system.update alone cannot change it", "settings-only", `[{"key":"agent_public_url","value":"wss://evil.example:8443/connect"}]`, http.StatusForbidden, oldURL},
		{"system.update alone cannot clear it", "settings-only", `[{"key":"agent_public_url","value":""}]`, http.StatusForbidden, oldURL},
		{"system.update alone may send it back unchanged", "settings-only", `[{"key":"agent_public_url","value":"` + oldURL + `"},{"key":"scanner_enabled","value":"false"}]`, http.StatusOK, oldURL},
		{"system.update with hosts.create changes it", "settings-hosts", `[{"key":"agent_public_url","value":"wss://new.example:8443/connect"}]`, http.StatusOK, "wss://new.example:8443/connect"},
		{"admin changes it", rbac.RoleAdmin, `[{"key":"agent_public_url","value":"wss://admin.example:8443/connect"}]`, http.StatusOK, "wss://admin.example:8443/connect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(ctx, settings.KeyAgentPublicURL, oldURL); err != nil {
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
			if got := store.Get(settings.KeyAgentPublicURL, ""); got != tt.wantURL {
				t.Errorf("agent_public_url %q, want %q", got, tt.wantURL)
			}
		})
	}
}

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

// Install commands are built from the agent URL, so changing it takes
// hosts.create on top of the system.update gate of PUT /settings, and
// every change is audited with the old and the new value.
func TestAgentURLChangeNeedsHostsCreate(t *testing.T) {
	const oldURL = "wss://agents.example.com:8443/connect"
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
	listAudit := func(t *testing.T) []audit.Entry {
		t.Helper()
		all, err := auditSvc.List(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return all
	}
	// List returns the newest entries first.
	newEntries := func(t *testing.T, before int) []audit.Entry {
		t.Helper()
		all := listAudit(t)
		var out []audit.Entry
		for _, e := range all[:len(all)-before] {
			if e.Action == "settings.agent_public_url_update" {
				out = append(out, e)
			}
		}
		return out
	}
	// DOCKMESH_AGENT_PUBLIC_URL wins over an empty saved value; the
	// settings page sends that empty value back, which is not a change.
	t.Run("empty saved value under an env override is unchanged", func(t *testing.T) {
		t.Setenv("DOCKMESH_AGENT_PUBLIC_URL", "wss://env.example:8443/connect")
		if err := store.Set(ctx, settings.KeyAgentPublicURL, ""); err != nil {
			t.Fatal(err)
		}
		before := len(listAudit(t))
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`[{"key":"agent_public_url","value":""}]`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokens["settings-only"])
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d %s, want 200", rr.Code, rr.Body)
		}
		if entries := newEntries(t, before); len(entries) != 0 {
			t.Errorf("audit entries %v for an unchanged agent URL", entries)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(ctx, settings.KeyAgentPublicURL, oldURL); err != nil {
				t.Fatal(err)
			}
			before := len(listAudit(t))
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
			entries := newEntries(t, before)
			if tt.wantURL == oldURL {
				if len(entries) != 0 {
					t.Errorf("audit entries %v for an unchanged agent URL", entries)
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("%d agent URL audit entries, want 1", len(entries))
			}
			var details struct{ Before, After string }
			if err := json.Unmarshal([]byte(entries[0].Details), &details); err != nil {
				t.Fatal(err)
			}
			if details.Before != oldURL || details.After != tt.wantURL {
				t.Errorf("audit details %s, want before %q after %q", entries[0].Details, oldURL, tt.wantURL)
			}
			if entries[0].UserID == "" {
				t.Errorf("audit entry has no actor")
			}
		})
	}
}

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
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestUpdateChannelSetting(t *testing.T) {
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
	if err := roles.CreateRole(ctx, rbac.RoleInput{Name: "hosts-only", Display: "Hosts only", Levels: rbac.Levels{"hosts": "manage"}}); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, role := range []string{"hosts-only", rbac.RoleAdmin} {
		tokens[role] = signIn(t, authSvc, roles, "u-"+role, role, rbac.Global)
	}
	router := NewRouter(h, authSvc, nil, true, nil)

	tests := []struct {
		name, role, start, value string
		wantCode                 int
		wantStored               string
		wantAudit                bool
	}{
		{"admin opts into pre-releases", rbac.RoleAdmin, "", "prerelease", http.StatusOK, "prerelease", true},
		{"admin goes back to stable", rbac.RoleAdmin, "prerelease", "stable", http.StatusOK, "stable", true},
		{"unchanged value is not audited", rbac.RoleAdmin, "prerelease", "prerelease", http.StatusOK, "prerelease", false},
		{"unknown channel is refused", rbac.RoleAdmin, "", "nightly", http.StatusBadRequest, "", false},
		{"without settings access", "hosts-only", "", "prerelease", http.StatusForbidden, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(ctx, selfupdate.ChannelKey, tt.start); err != nil {
				t.Fatal(err)
			}
			before, err := auditSvc.List(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`[{"key":"update_channel","value":"`+tt.value+`"}]`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokens[tt.role])
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != tt.wantCode {
				t.Fatalf("status %d %s, want %d", rr.Code, rr.Body, tt.wantCode)
			}
			if got := store.Stored(selfupdate.ChannelKey); got != tt.wantStored {
				t.Errorf("update_channel %q, want %q", got, tt.wantStored)
			}
			all, err := auditSvc.List(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			var entries []audit.Entry
			for _, e := range all[:len(all)-len(before)] {
				if e.Action == "settings.update_channel_update" {
					entries = append(entries, e)
				}
			}
			if !tt.wantAudit {
				if len(entries) != 0 {
					t.Errorf("unexpected audit entries %v", entries)
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("%d update channel audit entries, want 1", len(entries))
			}
			var details struct{ Before, After string }
			if err := json.Unmarshal([]byte(entries[0].Details), &details); err != nil {
				t.Fatal(err)
			}
			if details.Before != tt.start || details.After != tt.value || entries[0].UserID == "" {
				t.Errorf("audit entry %+v, want before %q after %q with an actor", entries[0], tt.start, tt.value)
			}
		})
	}
}

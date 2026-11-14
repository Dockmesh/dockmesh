package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestUpdateStatusSecurityOnlyForUpgraders(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	st := settings.NewStore(database)
	ctx := context.Background()
	for k, v := range map[string]string{
		"update_latest_version":   "v0.5.1",
		"update_release_manifest": `{"version":"0.5.1","severity":"security","changes":{"security":["fix"]}}`,
	} {
		if err := st.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handlers{SelfUpdate: selfupdate.New(st, "v0.5.0")}
	admin, _ := rbac.BuiltinRole(rbac.RoleAdmin)
	viewer, _ := rbac.BuiltinRole(rbac.RoleViewer)
	tests := []struct {
		name   string
		grant  rbac.Grant
		expose bool
	}{
		{"global admin", rbac.Grant{Perms: rbac.PermsFor(admin.Levels, admin.Extras)}, true},
		{"admin on a tag", rbac.Grant{Perms: rbac.PermsFor(admin.Levels, admin.Extras), Scopes: []rbac.Scope{{Type: rbac.ScopeHostTag, Value: "prod"}}}, false},
		{"viewer", rbac.Grant{Perms: rbac.PermsFor(viewer.Levels, viewer.Extras)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := middleware.Identity{UserID: "u", Access: &rbac.Access{Authenticated: true, Grants: []rbac.Grant{tt.grant}}}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/system/update-status", nil)
			rec := httptest.NewRecorder()
			h.GetUpdateStatus(rec, req.WithContext(middleware.WithIdentity(req.Context(), id)))
			var got selfupdate.Result
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if (got.Severity == "security") != tt.expose || (len(got.Security) > 0) != tt.expose {
				t.Errorf("severity %q security %v, want exposed=%v", got.Severity, got.Security, tt.expose)
			}
		})
	}
}

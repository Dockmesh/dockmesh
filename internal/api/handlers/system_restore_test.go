package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/setup"
)

func TestSetupRestoreRefusedOnceTaken(t *testing.T) {
	tests := []struct {
		name      string
		admin     bool
		slotTaken bool
	}{
		{name: "admin created after boot", admin: true},
		{name: "install already running", slotTaken: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.Open(filepath.Join(dir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			st, err := setup.NewFromDB(context.Background(), database)
			if err != nil || !st.Active() {
				t.Fatalf("setup not active on an empty database: %v", err)
			}
			if tt.admin {
				if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES ('u1', 'cli-admin', 'x', 'admin')`); err != nil {
					t.Fatal(err)
				}
			}
			if tt.slotTaken && !st.TryBeginCommit() {
				t.Fatal("could not take the install slot")
			}
			restarted := false
			h := &Handlers{
				DB:             database,
				SetupState:     st,
				RestoreConfig:  &config.Config{DBPath: filepath.Join(dir, "data", "dockmesh.db"), StacksRoot: filepath.Join(dir, "stacks")},
				RequestRestart: func() { restarted = true },
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/restore", strings.NewReader("--x--"))
			req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
			h.SetupRestore(rec, req)
			if rec.Code != http.StatusConflict || restarted {
				t.Fatalf("got %d (restart %v), want 409 and no restart: %s", rec.Code, restarted, rec.Body)
			}
			if tt.admin && st.Active() {
				t.Fatal("setup still active after an admin was found")
			}
		})
	}
}

func TestSystemRestoreIsAdminOnly(t *testing.T) {
	h := &Handlers{RestoreConfig: &config.Config{DBPath: filepath.Join(t.TempDir(), "dockmesh.db")}, RequestRestart: func() {}}
	for _, ep := range []struct {
		method string
		fn     http.HandlerFunc
	}{
		{http.MethodGet, h.GetStagedRestore},
		{http.MethodPost, h.StageSystemRestore},
		{http.MethodPost, h.ApplySystemRestore},
		{http.MethodDelete, h.DiscardSystemRestore},
	} {
		for _, id := range []middleware.Identity{
			{UserID: "u", Role: "host-admin"},
			{UserID: "u", Role: "admin", ScopeTags: []string{"prod"}},
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(ep.method, "/api/v1/restore/system", strings.NewReader(`{"run_id":1}`))
			ep.fn(rec, req.WithContext(middleware.WithIdentity(req.Context(), id)))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s as %s %v: got %d, want 403", ep.method, id.Role, id.ScopeTags, rec.Code)
			}
		}
	}
}

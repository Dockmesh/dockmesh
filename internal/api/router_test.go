package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestRotateEnrollTokenRequiresHostsCreate(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	roles := rbac.NewStore(database)
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := roles.CreateRole(ctx, rbac.RoleInput{Name: "host-viewer", Display: "Host viewer", Levels: rbac.Levels{"hosts": "view"}}); err != nil {
		t.Fatal(err)
	}
	prev := middleware.RBACStore
	middleware.RBACStore = roles
	t.Cleanup(func() { middleware.RBACStore = prev })

	authSvc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
	tokens := map[string]string{}
	for _, role := range []string{"host-viewer", "host-admin"} {
		if _, err := authSvc.CreateUser(ctx, role, "", "correct horse battery", role); err != nil {
			t.Fatal(err)
		}
		res, err := authSvc.Login(ctx, role, "correct horse battery", "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = res.AccessToken
	}
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}

	mgr, err := pki.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := agents.NewService(database, mgr, "http://srv:8080", "wss://srv:8443/connect")
	pending, err := svc.Create(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := svc.Create(ctx, "enrolled")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, agents.EnrollRequest{Token: enrolled.Token}); err != nil {
		t.Fatal(err)
	}

	h := handlers.New(handlers.Deps{Auth: authSvc, Agents: svc, Roles: roles})
	router := NewRouter(h, authSvc, nil, false, nil)

	cases := []struct {
		name, role, id string
		want           int
	}{
		{"hosts view on pending host", "host-viewer", pending.Agent.ID, http.StatusForbidden},
		{"hosts view on enrolled host", "host-viewer", enrolled.Agent.ID, http.StatusForbidden},
		{"hosts manage on pending host", "host-admin", pending.Agent.ID, http.StatusOK},
		{"hosts manage on enrolled host", "host-admin", enrolled.Agent.ID, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+c.id+"/rotate-token", nil)
			req.Header.Set("Authorization", "Bearer "+tokens[c.role])
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status %d, want %d (%s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

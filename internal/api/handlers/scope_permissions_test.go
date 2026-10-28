package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// newScopeEnv extends the authz env with the user, role, invite, stack and
// container routes this file exercises, wired like router.go.
func newScopeEnv(t *testing.T) *authzEnv {
	t.Helper()
	e := newAuthzEnv(t)
	e.h.Invites = invites.New(e.db, "http://dockmesh.test")
	e.h.Deployments = stacks.NewDeploymentStore(e.db)
	e.h.Backups = backup.NewService(e.db, nil, nil, nil, backup.SystemPaths{})
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermUsersCreate)).Post("/users", e.h.CreateUser)
		r.With(middleware.RequirePerm(rbac.PermUsersCreate)).Post("/invites", e.h.CreateInvite)
		r.With(middleware.RequirePerm(rbac.PermUsersUpdate)).Put("/users/{id}", e.h.UpdateUser)
		r.With(middleware.RequirePerm(rbac.PermUsersDelete)).Delete("/users/{id}", e.h.DeleteUser)
		r.With(middleware.RequirePerm(rbac.PermRolesCreate)).Post("/roles", e.h.CreateRole)
		r.With(middleware.RequirePerm(rbac.PermRolesUpdate)).Put("/roles/{name}", e.h.UpdateRole)
		r.With(middleware.RequirePerm(rbac.PermContainersView)).Get("/containers/{id}", e.h.InspectContainer)
		r.With(middleware.RequirePerm(rbac.PermStacksView)).Get("/api/v1/stacks/{name}/status", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	return e
}

func (e *authzEnv) role(name string, perms []rbac.Perm, scopes ...rbac.Scope) {
	e.t.Helper()
	if err := e.h.Roles.Create(context.Background(), rbac.RoleInput{Name: name, Display: name, Permissions: perms, Scopes: scopes}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *authzEnv) scopeUser(id string, tags ...string) {
	e.t.Helper()
	if _, err := e.h.Auth.UpdateUserScope(context.Background(), id, tags); err != nil {
		e.t.Fatal(err)
	}
}

func TestGrantsCannotExceedCaller(t *testing.T) {
	e := newScopeEnv(t)
	e.role("useradmin", []rbac.Perm{rbac.PermUsersView, rbac.PermUsersCreate, rbac.PermUsersUpdate, rbac.PermRolesCreate, rbac.PermRolesUpdate, rbac.PermContainersView})
	low := e.user("low", "useradmin")
	victim := e.user("victim", "viewer")
	e.user("boss", "admin")
	tagged := e.user("tagged", "useradmin")
	e.scopeUser(tagged.ID, "prod")
	lowTok := e.login("low").AccessToken
	taggedTok := e.login("tagged").AccessToken

	tests := []struct {
		name, method, path, bearer string
		body                       any
		want                       int
	}{
		{"self-promote to admin", http.MethodPut, "/users/" + low.ID, lowTok, map[string]any{"role": "admin"}, http.StatusForbidden},
		{"promote other to operator", http.MethodPut, "/users/" + victim.ID, lowTok, map[string]any{"role": "operator"}, http.StatusForbidden},
		{"create admin user", http.MethodPost, "/users", lowTok, map[string]any{"username": "x1", "password": testPassword, "role": "admin"}, http.StatusForbidden},
		{"create viewer-ish user", http.MethodPost, "/users", lowTok, map[string]any{"username": "x2", "password": testPassword, "role": "useradmin"}, http.StatusCreated},
		{"invite as admin", http.MethodPost, "/invites", lowTok, map[string]any{"role": "admin"}, http.StatusForbidden},
		{"invite unknown role", http.MethodPost, "/invites", lowTok, map[string]any{"role": "nope"}, http.StatusBadRequest},
		{"role with perms caller lacks", http.MethodPost, "/roles", lowTok, map[string]any{"name": "big", "display": "Big", "permissions": []string{"system.update"}}, http.StatusForbidden},
		{"edit own role upward", http.MethodPut, "/roles/useradmin", lowTok, map[string]any{"display": "u", "permissions": []string{"users.view", "users.update", "users.delete"}}, http.StatusForbidden},
		{"manage a stronger user", http.MethodPut, "/users/" + mustUserID(e, "boss"), lowTok, map[string]any{"role": "viewer"}, http.StatusForbidden},
		{"scoped caller unscopes self", http.MethodPut, "/users/" + tagged.ID, taggedTok, map[string]any{"scope_tags": []string{}}, http.StatusForbidden},
		{"scoped caller widens tags", http.MethodPut, "/users/" + tagged.ID, taggedTok, map[string]any{"scope_tags": []string{"prod", "dev"}}, http.StatusForbidden},
		{"scoped caller invites unscoped", http.MethodPost, "/invites", taggedTok, map[string]any{"role": "useradmin"}, http.StatusForbidden},
		{"scoped caller invites within scope", http.MethodPost, "/invites", taggedTok, map[string]any{"role": "useradmin", "scope_tags": []string{"prod"}}, http.StatusCreated},
		{"omitted scope keeps tags", http.MethodPut, "/users/" + tagged.ID, taggedTok, map[string]any{"display_name": "T"}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tt.bearer, tt.body)
			if rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
	u, err := e.h.Auth.GetUser(context.Background(), tagged.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.ScopeTags) != 1 || u.ScopeTags[0] != "prod" {
		t.Errorf("scope tags after update without scope_tags = %v, want [prod]", u.ScopeTags)
	}
}

func mustUserID(e *authzEnv, username string) string {
	e.t.Helper()
	users, err := e.h.Auth.ListUsers(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == username {
			return u.ID
		}
	}
	e.t.Fatalf("no user %s", username)
	return ""
}

func TestLastAdminIsProtected(t *testing.T) {
	e := newScopeEnv(t)
	first := e.user("first", "admin")
	tok := e.login("first").AccessToken

	if rr := e.do(http.MethodPut, "/users/"+first.ID, tok, map[string]any{"role": "viewer"}); rr.Code != http.StatusConflict {
		t.Fatalf("demote last admin: %d %s", rr.Code, rr.Body)
	}
	e.role("superuser", []rbac.Perm{rbac.PermSystemUpdate, rbac.PermUsersView, rbac.PermUsersCreate, rbac.PermUsersUpdate, rbac.PermUsersDelete, rbac.PermRolesUpdate})
	second := e.user("second", "superuser")
	if rr := e.do(http.MethodPut, "/roles/superuser", tok, map[string]any{"display": "s", "permissions": []string{"users.view"}}); rr.Code != http.StatusOK {
		t.Fatalf("narrow a custom admin role while another admin exists: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodPut, "/users/"+second.ID, tok, map[string]any{"role": "admin"}); rr.Code != http.StatusOK {
		t.Fatalf("promote second: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodPut, "/users/"+first.ID, tok, map[string]any{"role": "viewer"}); rr.Code != http.StatusOK {
		t.Fatalf("demote with another admin left: %d %s", rr.Code, rr.Body)
	}
	secondTok := e.login("second").AccessToken
	if rr := e.do(http.MethodDelete, "/users/"+first.ID, secondTok, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete non-admin: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodPut, "/users/"+second.ID, secondTok, map[string]any{"role": "viewer"}); rr.Code != http.StatusConflict {
		t.Fatalf("demote the remaining admin: %d %s", rr.Code, rr.Body)
	}
}

func TestReadEndpointsEnforceScope(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	e.role("stack-only", []rbac.Perm{rbac.PermContainersView, rbac.PermStacksView}, rbac.Scope{ScopeType: "stack", ScopeValue: "web"})
	e.user("free", "viewer")
	tagged := e.user("tagged", "viewer")
	e.scopeUser(tagged.ID, "prod")
	e.user("stacky", "stack-only")
	if err := e.h.Deployments.Set(ctx, "db", "agent-1", "running"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Deployments.Set(ctx, "web", "agent-1", "running"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		user, path string
		want       int
	}{
		// No Docker in tests: a request that passes scope fails the inspect.
		{"free", "/containers/c1", http.StatusNotFound},
		{"tagged", "/containers/c1", http.StatusForbidden},
		{"stacky", "/containers/c1", http.StatusForbidden},
		{"free", "/api/v1/stacks/db/status", http.StatusNoContent},
		{"tagged", "/api/v1/stacks/db/status", http.StatusForbidden},
		{"stacky", "/api/v1/stacks/db/status", http.StatusForbidden},
		{"stacky", "/api/v1/stacks/web/status", http.StatusNoContent},
		{"tagged", "/api/v1/stacks/undeployed/status", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.user+tt.path, func(t *testing.T) {
			rr := e.do(http.MethodGet, tt.path, e.login(tt.user).AccessToken, nil)
			if rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}

func TestFilterHostsByScopeFailsClosed(t *testing.T) {
	h := &Handlers{}
	hosts := []host.Host{host.NewLocal(nil)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := h.filterHostsByScope(req, hosts); len(got) != 1 {
		t.Errorf("unscoped caller: %d hosts, want 1", len(got))
	}
	scoped := req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: "u", Role: "viewer", ScopeTags: []string{"prod"}}))
	if got := h.filterHostsByScope(scoped, hosts); len(got) != 0 {
		t.Errorf("scoped caller without host tags service: %d hosts, want 0", len(got))
	}
	h.HostTags = hosttags.New(nil)
	if got := h.filterHostsByScope(scoped, hosts); len(got) != 0 {
		t.Errorf("scoped caller, untagged host: %d hosts, want 0", len(got))
	}
}

func TestArchiveAccess(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id) VALUES (1, 'stack-job', 'local', 'local')`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id) VALUES (2, 'remote-job', 'local', 'agent-1')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	e.user("boss", "admin")
	e.user("hadmin", "host-admin")
	tagged := e.user("thadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")

	system := &backup.Run{JobID: 1, Sources: []backup.Source{{Type: "system", Name: "system"}}}
	stack := &backup.Run{JobID: 1, Sources: []backup.Source{{Type: "stack", Name: "web"}}}
	remote := &backup.Run{JobID: 2, Sources: []backup.Source{{Type: "stack", Name: "web"}}}
	orphan := &backup.Run{JobID: 99, Sources: []backup.Source{{Type: "volume", Name: "v"}}}
	tests := []struct {
		user string
		run  *backup.Run
		want bool
	}{
		{"boss", system, true},
		{"hadmin", system, false},
		{"hadmin", stack, true},
		{"thadmin", stack, false},
		{"thadmin", remote, false},
		{"hadmin", orphan, false},
		{"boss", orphan, true},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			claims, err := e.h.Auth.Validate(ctx, e.login(tt.user).AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: claims.UserID, Role: claims.Role, ScopeTags: claims.ScopeTags}))
			rr := httptest.NewRecorder()
			if got := e.h.requireArchiveAccess(rr, req, tt.run); got != tt.want {
				var body map[string]any
				_ = json.Unmarshal(rr.Body.Bytes(), &body)
				t.Errorf("allowed=%v, want %v (%v)", got, tt.want, body)
			}
		})
	}
}

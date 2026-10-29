package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notify"
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
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			claims, err := e.h.Auth.Validate(req.Context(), e.login(tt.user).AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			req = req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: claims.UserID, Role: claims.Role, ScopeTags: claims.ScopeTags}))
			rr := httptest.NewRecorder()
			if got := e.h.requireRunAccess(rr, req, tt.run, rbac.PermBackupsDownload); got != tt.want {
				var body map[string]any
				_ = json.Unmarshal(rr.Body.Bytes(), &body)
				t.Errorf("allowed=%v, want %v (%v)", got, tt.want, body)
			}
		})
	}
}

func TestCredentialsAreWriteOnly(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	e.h.BackupTargets = targets.NewTargetStore(e.db)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/targets", e.h.ListBackupTargets)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Put("/backups/targets/{id}", e.h.UpdateBackupTarget)
	})
	stored, err := e.h.BackupTargets.Create(ctx, targets.TargetInput{Name: "nas", Type: "sftp", Config: map[string]any{"host": "nas", "password": "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	e.user("viewer1", "viewer")
	e.user("boss", "admin")

	rr := e.do(http.MethodGet, "/backups/targets", e.login("viewer1").AccessToken, nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "hunter2") || !strings.Contains(rr.Body.String(), `"password_set":true`) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var listed []targets.StoredTarget
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("decode list: %v %s", err, rr.Body)
	}
	// Sending the redacted config back must not wipe the stored password.
	rr = e.do(http.MethodPut, "/backups/targets/"+strconv.FormatInt(stored.ID, 10), e.login("boss").AccessToken,
		map[string]any{"name": "nas", "type": "sftp", "config": listed[0].Config})
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatalf("update: %d %s", rr.Code, rr.Body)
	}
	after, err := e.h.BackupTargets.Get(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := after.Config.(map[string]any); cfg["password"] != "hunter2" || cfg["password_set"] != nil {
		t.Errorf("stored config after round trip = %v", after.Config)
	}

	job := redactJob(&backup.Job{TargetType: "s3", TargetConfig: map[string]any{"access_key": "AK", "secret_key": "SK"}})
	if cfg := job.TargetConfig.(map[string]any); cfg["secret_key"] != "" || cfg["access_key"] != "AK" {
		t.Errorf("job config = %v", cfg)
	}
	ch := redactChannel(&notify.Channel{Type: "slack", Config: json.RawMessage(`{"url":"https://hooks.slack.com/services/T/B/x"}`)})
	if strings.Contains(string(ch.Config), "hooks.slack.com") {
		t.Errorf("slack webhook url leaked: %s", ch.Config)
	}
}

func TestReviewFollowUps(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsCreate)).Post("/backups/jobs", e.h.CreateBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Post("/backups/jobs/{id}/run", e.h.RunBackupJob)
		r.With(middleware.RequirePerm(rbac.PermRolesDelete)).Delete("/roles/{name}", e.h.DeleteRole)
		r.With(middleware.RequirePerm(rbac.PermStacksView)).Get("/api/v1/stacks/{name}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	e.router.Post("/invite/{token}/accept", e.h.AcceptInvite)
	if _, err := e.db.Exec(`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (7, 'sys', 'local', 'local', '[{"type":"system","name":"system"}]')`); err != nil {
		t.Fatal(err)
	}
	boss := e.user("boss", "admin")
	e.user("hadmin", "host-admin")
	e.role("stack-only", []rbac.Perm{rbac.PermStacksView}, rbac.Scope{ScopeType: "stack", ScopeValue: "web"})
	e.user("stacky", "stack-only")
	e.role("tagged-roles", []rbac.Perm{rbac.PermRolesUpdate, rbac.PermRolesDelete, rbac.PermStacksView})
	tagged := e.user("rolemgr", "tagged-roles")
	e.scopeUser(tagged.ID, "prod")
	e.role("victim-role", []rbac.Perm{rbac.PermStacksView})
	hadmin := e.login("hadmin").AccessToken
	bossTok := e.login("boss").AccessToken

	tests := []struct {
		name, method, path, bearer string
		body                       any
		want                       int
	}{
		{"host-admin creates system job", http.MethodPost, "/backups/jobs", hadmin,
			map[string]any{"name": "x", "target_type": "local", "sources": []map[string]string{{"type": "system", "name": "system"}}}, http.StatusForbidden},
		{"host-admin runs system job", http.MethodPost, "/backups/jobs/7/run", hadmin, nil, http.StatusForbidden},
		{"scoped caller deletes a role", http.MethodDelete, "/roles/victim-role", e.login("rolemgr").AccessToken, nil, http.StatusForbidden},
		{"stack role reads an undeployed other stack", http.MethodGet, "/api/v1/stacks/bar", e.login("stacky").AccessToken, nil, http.StatusForbidden},
		{"stack role reads its undeployed stack", http.MethodGet, "/api/v1/stacks/web", e.login("stacky").AccessToken, nil, http.StatusNoContent},
		{"only admin scopes itself", http.MethodPut, "/users/" + boss.ID, bossTok, map[string]any{"scope_tags": []string{"x"}}, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tt.bearer, tt.body)
			if rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}

	// An invite stops working once its creator may no longer grant it.
	inviter := e.user("inviter", "admin")
	inv, err := e.h.Invites.Create(ctx, invites.CreateInput{Role: "admin", TTL: time.Hour, CreatedBy: inviter.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Auth.UpdateUser(ctx, inviter.ID, "", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	rr := e.do(http.MethodPost, "/invite/"+inv.Token+"/accept", "", map[string]any{"username": "mole", "password": testPassword})
	if rr.Code != http.StatusGone {
		t.Errorf("accept after creator demotion: %d %s", rr.Code, rr.Body)
	}
}

func TestSystemBackupsStayAdminOnly(t *testing.T) {
	e := newScopeEnv(t)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs/{id}/log", e.h.GetBackupRunLog)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Put("/backups/jobs/{id}", e.h.UpdateBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Post("/backups/jobs/{id}/run", e.h.RunBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Post("/backups/jobs/{id}/review/{mode}", e.h.AcknowledgeBackupJobReview)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Put("/backups/system/enabled", e.h.SetBackupEnabled)
		r.With(middleware.RequirePerm(rbac.PermBackupsDelete)).Delete("/backups/jobs/{id}", e.h.DeleteBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/restore", e.h.RestoreBackup)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/restore-stack", e.h.RestoreStackBackup)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/verify", e.h.VerifyBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/restore/verify", e.h.VerifyUploadedBackup)
	})
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (7, 'sys', 'local', 'local', '[{"type":"system","name":"system"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (1, 'web', 'local', 'local', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (2, 'remote', 'local', 'agent-1', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (3, 'spare', 'local', 'local', '[{"type":"volume","name":"v"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (10, 7, 'sys', 'success', '[{"type":"system","name":"system"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (11, 2, 'remote', 'success', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (12, 1, 'web', 'success', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (13, 1, 'web', 'success', '[{"type":"stack","name":"web"},{"type":"system","name":"system"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	e.user("boss", "admin")
	e.user("hadmin", "host-admin")
	tagged := e.user("thadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")
	e.user("look", "viewer")
	boss := e.login("boss").AccessToken
	hadmin := e.login("hadmin").AccessToken
	thadmin := e.login("thadmin").AccessToken
	look := e.login("look").AccessToken
	vol := map[string]string{"dest_volume": "loot"}
	plainJob := map[string]any{"name": "sys", "target_type": "local", "host_id": "local", "sources": []map[string]string{{"type": "volume", "name": "v"}}}

	tests := []struct {
		name, method, path, bearer string
		body                       any
		want                       int
	}{
		{"host-admin restores system run", http.MethodPost, "/backups/runs/10/restore", hadmin, vol, http.StatusForbidden},
		{"host-admin stack-restores system run", http.MethodPost, "/backups/runs/10/restore-stack", hadmin, nil, http.StatusForbidden},
		{"admin restores system run into a volume", http.MethodPost, "/backups/runs/10/restore", boss, vol, http.StatusConflict},
		{"admin stack-restores system run", http.MethodPost, "/backups/runs/10/restore-stack", boss, nil, http.StatusConflict},
		{"system source behind a stack source", http.MethodPost, "/backups/runs/13/restore", boss, vol, http.StatusConflict},
		{"out-of-scope stack restore", http.MethodPost, "/backups/runs/11/restore-stack", thadmin, nil, http.StatusForbidden},
		{"host-admin toggles system backup", http.MethodPut, "/backups/system/enabled", hadmin, map[string]bool{"enabled": true}, http.StatusForbidden},
		{"admin toggles system backup", http.MethodPut, "/backups/system/enabled", boss, map[string]bool{"enabled": false}, http.StatusOK},
		{"host-admin rewrites system job", http.MethodPut, "/backups/jobs/7", hadmin, plainJob, http.StatusForbidden},
		{"host-admin runs system job", http.MethodPost, "/backups/jobs/7/run", hadmin, nil, http.StatusForbidden},
		{"host-admin reviews system job", http.MethodPost, "/backups/jobs/7/review/disable", hadmin, nil, http.StatusForbidden},
		{"host-admin deletes system job", http.MethodDelete, "/backups/jobs/7", hadmin, nil, http.StatusForbidden},
		{"host-admin deletes own job", http.MethodDelete, "/backups/jobs/3", hadmin, nil, http.StatusNoContent},
		{"viewer reads system run log", http.MethodGet, "/backups/runs/10/log", look, nil, http.StatusForbidden},
		{"admin reads system run log", http.MethodGet, "/backups/runs/10/log", boss, nil, http.StatusOK},
		{"host-admin reads in-scope log", http.MethodGet, "/backups/runs/12/log", hadmin, nil, http.StatusOK},
		{"scoped host-admin reads remote log", http.MethodGet, "/backups/runs/11/log", thadmin, nil, http.StatusForbidden},
		{"host-admin verifies system run", http.MethodPost, "/backups/runs/10/verify", hadmin, nil, http.StatusForbidden},
		{"host-admin verifies an upload", http.MethodPost, "/restore/verify", hadmin, nil, http.StatusForbidden},
		{"admin verifies an empty upload", http.MethodPost, "/restore/verify", boss, nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tt.bearer, tt.body)
			if rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
	if _, err := e.h.Backups.GetJob(context.Background(), 7); err != nil {
		t.Errorf("system job after host-admin delete: %v", err)
	}
}

func TestBackupJobsKeepTheirOwnArchives(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	svc := e.h.Backups
	// A legacy non-system job carrying the default name, created before
	// names were checked, must not stand in for the system job.
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources, enabled) VALUES (1, 'dockmesh-system', 'local', 'local', '[{"type":"volume","name":"v"}]', 1)`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources, enabled) VALUES (5, 'nightly', 'local', 'local', '[{"type":"system","name":"dockmesh"}]', 1)`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	vol := []backup.Source{{Type: "volume", Name: "data"}}
	sys := []backup.Source{{Type: "system", Name: "dockmesh"}}
	web, err := svc.CreateJob(ctx, backup.JobInput{Name: "web", TargetType: "local", Sources: vol})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		id      int64
		in      backup.JobInput
		wantErr bool
	}{
		{"same folder as another job", 0, backup.JobInput{Name: "Web!", Sources: vol}, true},
		{"name without letters or digits", 0, backup.JobInput{Name: "!!!", Sources: vol}, true},
		{"system name on a volume job", 0, backup.JobInput{Name: "Dockmesh-System", Sources: vol}, true},
		{"system name on a second system job", 0, backup.JobInput{Name: "Dockmesh System", Sources: sys}, true},
		{"rename onto another job", web.ID, backup.JobInput{Name: "nightly", Sources: vol}, true},
		{"keep own name", web.ID, backup.JobInput{Name: "web", Sources: vol}, false},
		{"distinct name", 0, backup.JobInput{Name: "web-db", Sources: vol}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.TargetType = "local"
			var err error
			if tt.id == 0 {
				_, err = svc.CreateJob(ctx, tt.in)
			} else {
				_, err = svc.UpdateJob(ctx, tt.id, tt.in)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error %v", err, tt.wantErr)
			}
		})
	}

	if err := svc.SetDefaultJobEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{1: true, 5: false} {
		j, err := svc.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Enabled != want {
			t.Errorf("job %d enabled = %v, want %v", id, j.Enabled, want)
		}
	}
	st, err := svc.LastSystemRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Enabled {
		t.Errorf("system status = %+v, want the disabled system job", st)
	}
}

func TestBackupNameCollisionNotice(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	n := notices.NewService(e.db)
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, sources) VALUES (1, 'web', 'local', '[{"type":"volume","name":"a"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, sources) VALUES (2, 'Web!', 'local', '[{"type":"volume","name":"b"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, sources) VALUES (3, 'Dockmesh System', 'local', '[{"type":"volume","name":"c"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, sources) VALUES (4, 'dockmesh-system', 'local', '[{"type":"system","name":"dockmesh"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	notice := func() *notices.Notice {
		t.Helper()
		if err := e.h.Backups.SyncNameCollisionNotice(ctx, n); err != nil {
			t.Fatal(err)
		}
		open, err := n.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range open {
			if open[i].Key == "backups.job-name-collision" {
				return &open[i]
			}
		}
		return nil
	}

	steps := []struct {
		name, rename string
		id           int
		want         []string
	}{
		{"both collisions listed", "", 0, []string{`"Web!", "web"`, `"Dockmesh System" is not a system backup`, `"Dockmesh System", "dockmesh-system"`}},
		{"one group renamed", "web-old", 2, []string{`"Dockmesh System" is not a system backup`}},
		{"all renamed", "misc", 3, nil},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.rename != "" {
				if _, err := e.db.Exec(`UPDATE backup_jobs SET name = ? WHERE id = ?`, st.rename, st.id); err != nil {
					t.Fatal(err)
				}
			}
			got := notice()
			if st.want == nil {
				if got != nil {
					t.Fatalf("notice still open: %s", got.Body)
				}
				return
			}
			if got == nil {
				t.Fatal("no notice")
			}
			for _, w := range st.want {
				if !strings.Contains(got.Body, w) {
					t.Errorf("body %q lacks %q", got.Body, w)
				}
			}
			if st.id != 0 && strings.Contains(got.Body, `"web"`) {
				t.Errorf("stale body after rename: %s", got.Body)
			}
		})
	}
}

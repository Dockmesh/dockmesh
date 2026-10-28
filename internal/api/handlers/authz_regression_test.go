package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

const testPassword = "correct horse battery"

type authzEnv struct {
	t      *testing.T
	db     *sql.DB
	h      *Handlers
	router chi.Router
}

// newAuthzEnv wires the real auth middleware, RBAC store, API tokens and
// notices against a migrated database, with the same route split as
// router.go: REST routes behind NewAuth, WebSocket routes outside it.
func newAuthzEnv(t *testing.T) *authzEnv {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authSvc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
	roles := rbac.NewStore(database)
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}
	tokens := apitokens.New(database)

	prevRBAC, prevTokens := middleware.RBACStore, middleware.APITokensStore
	middleware.RBACStore, middleware.APITokensStore = roles, tokens
	t.Cleanup(func() { middleware.RBACStore, middleware.APITokensStore = prevRBAC, prevTokens })

	h := &Handlers{
		Auth:          authSvc,
		Roles:         roles,
		APITokens:     tokens,
		Notices:       notices.NewService(database),
		Hosts:         host.NewRegistry(nil, nil),
		RBACv2Enforce: true,
	}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(authSvc))
		r.Get("/me", h.Me)
		r.Post("/ws/ticket", h.WSTicket)
		r.Delete("/mfa", h.MFADisable)
		r.Put("/users/{id}/password", h.ChangeUserPassword)
		r.Get("/settings/api-tokens", h.ListAPITokens)
		r.Post("/settings/api-tokens", h.CreateAPIToken)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Get("/needs-deploy", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	r.Get("/ws/logs/{id}", h.WSLogs)
	r.Get("/ws/exec/{id}", h.WSExec)
	r.Get("/ws/stats/{id}", h.WSStats)
	r.Get("/ws/events", h.WSEvents)
	return &authzEnv{t: t, db: database, h: h, router: r}
}

func (e *authzEnv) user(name, role string) *auth.User {
	e.t.Helper()
	u, err := e.h.Auth.CreateUser(context.Background(), name, "", testPassword, role)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *authzEnv) login(name string) *auth.LoginResult {
	e.t.Helper()
	res, err := e.h.Auth.Login(context.Background(), name, testPassword, "test", "127.0.0.1")
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *authzEnv) do(method, path, bearer string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			e.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	e.router.ServeHTTP(rr, req)
	return rr
}

func (e *authzEnv) ticket(bearer, perm string) string {
	e.t.Helper()
	rr := e.do(http.MethodPost, "/ws/ticket?for="+perm, bearer, nil)
	if rr.Code != http.StatusOK {
		e.t.Fatalf("ticket for %s: %d %s", perm, rr.Code, rr.Body)
	}
	var out struct{ Ticket string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Ticket
}

func (e *authzEnv) apiToken(bearer, role string) string {
	e.t.Helper()
	rr := e.do(http.MethodPost, "/settings/api-tokens", bearer, map[string]any{"name": "ci", "role": role})
	if rr.Code != http.StatusCreated {
		e.t.Fatalf("create token: %d %s", rr.Code, rr.Body)
	}
	var out struct{ Token string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Token
}

func TestMFADisableBypassIsClosed(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	u := e.user("alice", "admin")
	enr, err := e.h.Auth.StartTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enr.Secret, time.Now())
	if _, err := e.h.Auth.VerifyTOTPEnrollment(ctx, u.ID, code); err != nil {
		t.Fatal(err)
	}

	pending := e.login("alice")
	if !pending.MFARequired || pending.MFAToken == "" {
		t.Fatal("expected an MFA challenge")
	}
	if rr := e.do(http.MethodDelete, "/mfa", pending.MFAToken, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("mfa-pending token as Bearer: %d, want 401", rr.Code)
	}
	if rr := e.do(http.MethodGet, "/me", pending.MFAToken, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("mfa-pending token on /me: %d, want 401", rr.Code)
	}

	code, _ = totp.GenerateCode(enr.Secret, time.Now())
	full, err := e.h.Auth.VerifyLoginMFA(ctx, pending.MFAToken, code, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodDelete, "/mfa", full.AccessToken, nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("disable without code: %d, want 400", rr.Code)
	}
	if on, _ := e.h.Auth.HasMFA(ctx, u.ID); !on {
		t.Fatal("MFA disabled without a code")
	}
	code, _ = totp.GenerateCode(enr.Secret, time.Now())
	if rr := e.do(http.MethodDelete, "/mfa", full.AccessToken, map[string]string{"code": code}); rr.Code != http.StatusNoContent {
		t.Fatalf("disable with code: %d %s", rr.Code, rr.Body)
	}
}

// Every WS endpoint must run as the ticket's identity: a user scoped away
// from the local host, or a role scoped to another host, gets 403 before
// the upgrade. Getting past auth shows up as 400 (httptest can't upgrade).
func TestWSHandlersEnforceScope(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	if err := e.h.Roles.Create(ctx, rbac.RoleInput{
		Name:        "remote-ops",
		Display:     "Remote ops",
		Permissions: []rbac.Perm{rbac.PermContainersView, rbac.PermContainersLogs, rbac.PermContainersExec, rbac.PermSystemView},
		Scopes:      []rbac.Scope{{ScopeType: "host", ScopeValue: "agent-elsewhere"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.user("free", "operator")
	tagged := e.user("tagged", "operator")
	if _, err := e.h.Auth.UpdateUserScope(ctx, tagged.ID, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.user("rolescoped", "remote-ops")

	endpoints := []struct{ perm, path string }{
		{"containers.exec", "/ws/exec/c1"},
		{"containers.logs", "/ws/logs/c1"},
		{"containers.view", "/ws/stats/c1"},
	}
	tests := []struct {
		user string
		want int
	}{
		{"free", http.StatusBadRequest},
		{"tagged", http.StatusForbidden},
		{"rolescoped", http.StatusForbidden},
	}
	for _, tt := range tests {
		access := e.login(tt.user).AccessToken
		for _, ep := range endpoints {
			t.Run(tt.user+ep.path, func(t *testing.T) {
				rr := e.do(http.MethodGet, ep.path+"?ticket="+e.ticket(access, ep.perm), "", nil)
				if rr.Code != tt.want {
					t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
				}
			})
		}
	}
}

func TestWSTicketsAreSingleUseAndBound(t *testing.T) {
	e := newAuthzEnv(t)
	e.user("op", "operator")
	access := e.login("op").AccessToken

	for _, ep := range []struct{ perm, path string }{
		{"containers.exec", "/ws/exec/c1"},
		{"containers.logs", "/ws/logs/c1"},
		{"containers.view", "/ws/stats/c1"},
		{"system.view", "/ws/events"},
	} {
		t.Run(ep.path, func(t *testing.T) {
			tk := e.ticket(access, ep.perm)
			if rr := e.do(http.MethodGet, ep.path+"?ticket="+tk, "", nil); rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Fatalf("first use rejected: %d", rr.Code)
			}
			if rr := e.do(http.MethodGet, ep.path+"?ticket="+tk, "", nil); rr.Code != http.StatusUnauthorized {
				t.Errorf("replay: %d, want 401", rr.Code)
			}
		})
	}

	logs := e.ticket(access, "containers.logs")
	if rr := e.do(http.MethodGet, "/ws/exec/c1?ticket="+logs, "", nil); rr.Code != http.StatusForbidden {
		t.Errorf("logs ticket on exec: %d, want 403", rr.Code)
	}
	if rr := e.do(http.MethodGet, "/ws/exec/c1?ticket="+access, "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("access token as ticket: %d, want 401", rr.Code)
	}
	e.user("v", "viewer")
	if rr := e.do(http.MethodPost, "/ws/ticket?for=containers.exec", e.login("v").AccessToken, nil); rr.Code != http.StatusForbidden {
		t.Errorf("viewer exec ticket: %d, want 403", rr.Code)
	}
}

func TestAPITokenFollowsOwner(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	owner := e.user("owner", "operator")
	token := e.apiToken(e.login("owner").AccessToken, "operator")

	if rr := e.do(http.MethodGet, "/needs-deploy", token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("owned token, owner is operator: %d", rr.Code)
	}

	if _, err := e.h.Auth.UpdateUser(ctx, owner.ID, "", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodGet, "/needs-deploy", token, nil); rr.Code != http.StatusForbidden {
		t.Errorf("owner demoted to viewer: %d, want 403", rr.Code)
	}
	if rr := e.do(http.MethodPost, "/ws/ticket?for=containers.exec", token, nil); rr.Code != http.StatusForbidden {
		t.Errorf("exec ticket after demotion: %d, want 403", rr.Code)
	}

	if _, err := e.h.Auth.UpdateUser(ctx, owner.ID, "", "operator", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Auth.UpdateUserScope(ctx, owner.ID, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	tk := e.ticket(token, "containers.exec")
	if rr := e.do(http.MethodGet, "/ws/exec/c1?ticket="+tk, "", nil); rr.Code != http.StatusForbidden {
		t.Errorf("owner scoped away from host: %d, want 403", rr.Code)
	}

	if err := e.h.Auth.DeleteUser(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodGet, "/me", token, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("owner deleted: %d, want 401", rr.Code)
	}
}

func TestLegacyTokenKeepsWorkingAndRaisesNotice(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	e.user("admin", "admin")
	adminAccess := e.login("admin").AccessToken
	token := e.apiToken(adminAccess, "operator")

	// What the migration does to a token whose creator wasn't recorded.
	if _, err := e.db.Exec(`UPDATE api_tokens SET legacy = 1, created_by_user_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodGet, "/needs-deploy", token, nil); rr.Code != http.StatusNoContent {
		t.Errorf("legacy operator token: %d, want 204", rr.Code)
	}
	if rr := e.do(http.MethodPost, "/settings/api-tokens", token, map[string]any{"name": "x", "role": "viewer"}); rr.Code != http.StatusForbidden {
		t.Errorf("legacy token minting a token: %d, want 403", rr.Code)
	}

	rr := e.do(http.MethodGet, "/settings/api-tokens", adminAccess, nil)
	var list []apitokens.Token
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Legacy {
		t.Fatalf("list = %+v, want one legacy token", list)
	}
	open, err := e.h.Notices.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("open notices = %d, want the legacy-token notice", len(open))
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	e := newAuthzEnv(t)
	u := e.user("pat", "operator")
	current, other := e.login("pat"), e.login("pat")

	rr := e.do(http.MethodPut, "/users/"+u.ID+"/password", current.AccessToken,
		map[string]string{"current_password": testPassword, "password": "another long password"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodGet, "/me", current.AccessToken, nil); rr.Code != http.StatusOK {
		t.Errorf("own session ended: %d", rr.Code)
	}
	if rr := e.do(http.MethodGet, "/me", other.AccessToken, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("other session still valid: %d", rr.Code)
	}
	if _, err := e.h.Auth.Refresh(context.Background(), other.RefreshToken); err == nil {
		t.Error("other session can still refresh")
	}

	e.user("root", "admin")
	admin := e.login("root").AccessToken
	if rr := e.do(http.MethodPut, "/users/"+u.ID+"/password", admin,
		map[string]string{"password": "reset by an admin"}); rr.Code != http.StatusNoContent {
		t.Fatalf("admin reset: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodGet, "/me", current.AccessToken, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("admin reset kept a session: %d", rr.Code)
	}
}

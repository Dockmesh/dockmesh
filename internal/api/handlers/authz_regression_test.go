package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

const testPassword = "correct horse battery"

type authzEnv struct {
	t      *testing.T
	db     *sql.DB
	h      *Handlers
	router chi.Router
	// wsEnded receives when the /ws/test handler returns.
	wsEnded chan struct{}
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

		mfaDisableLimiter: ratelimit.New(5, time.Minute, time.Minute),
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
		r.Delete("/settings/api-tokens/{id}", h.RevokeAPIToken)
		r.Delete("/users/{id}/mfa", h.MFAReset)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Get("/needs-deploy", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
	r.Get("/ws/logs/{id}", h.WSLogs)
	r.Get("/ws/exec/{id}", h.WSExec)
	r.Get("/ws/stats/{id}", h.WSStats)
	r.Get("/ws/events", h.WSEvents)
	// Minimal WS endpoint with the real auth + watcher, for tests that need
	// a live connection without Docker.
	wsEnded := make(chan struct{}, 1)
	r.Get("/ws/test", func(w http.ResponseWriter, r *http.Request) {
		defer func() { wsEnded <- struct{}{} }()
		r, ok := h.wsAuth(w, r, rbac.PermContainersExec)
		if !ok {
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		h.wsWatch(r, conn, rbac.PermContainersExec, "local")
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	return &authzEnv{t: t, db: database, h: h, router: r, wsEnded: wsEnded}
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
	// Each step below needs a fresh code: an accepted code can't be reused.
	code, _ := totp.GenerateCode(enr.Secret, time.Now().Add(-30*time.Second))
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
	code, _ = totp.GenerateCode(enr.Secret, time.Now().Add(30*time.Second))
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

func (e *authzEnv) apiTokenWith(bearer string, body map[string]any) (string, int64) {
	e.t.Helper()
	rr := e.do(http.MethodPost, "/settings/api-tokens", bearer, body)
	if rr.Code != http.StatusCreated {
		e.t.Fatalf("create token: %d %s", rr.Code, rr.Body)
	}
	var out struct {
		Token string
		ID    int64
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Token, out.ID
}

func TestManageOthersTokenCantMintPastItsRole(t *testing.T) {
	e := newAuthzEnv(t)
	if err := e.h.Roles.Create(context.Background(), rbac.RoleInput{
		Name:    "token-rotator",
		Display: "Token rotator",
		Permissions: []rbac.Perm{rbac.PermTokensView, rbac.PermTokensCreate,
			rbac.PermTokensDelete, rbac.PermTokensManageOthers},
	}); err != nil {
		t.Fatal(err)
	}
	e.user("root", "admin")
	rotator := e.apiToken(e.login("root").AccessToken, "token-rotator")
	if rr := e.do(http.MethodPost, "/settings/api-tokens", rotator, map[string]any{"name": "x", "role": "admin"}); rr.Code != http.StatusForbidden {
		t.Errorf("rotator minting an admin token: %d, want 403", rr.Code)
	}
}

func TestAdminMFAResetRefusesOwnAccount(t *testing.T) {
	e := newAuthzEnv(t)
	u := e.user("root", "admin")
	access := e.login("root").AccessToken
	if rr := e.do(http.MethodDelete, "/users/"+u.ID+"/mfa", access, nil); rr.Code != http.StatusBadRequest {
		t.Errorf("self reset via admin route: %d, want 400", rr.Code)
	}
	other := e.user("other", "viewer")
	if rr := e.do(http.MethodDelete, "/users/"+other.ID+"/mfa", access, nil); rr.Code != http.StatusNoContent {
		t.Errorf("resetting another user: %d, want 204", rr.Code)
	}
}

func TestMFADisableIsRateLimited(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	u := e.user("mallory", "viewer")
	access := e.login("mallory").AccessToken
	enr, err := e.h.Auth.StartTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enr.Secret, time.Now())
	if _, err := e.h.Auth.VerifyTOTPEnrollment(ctx, u.ID, code); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if rr := e.do(http.MethodDelete, "/mfa", access, map[string]string{"code": "000000"}); rr.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d, want 400", i+1, rr.Code)
		}
	}
	code, _ = totp.GenerateCode(enr.Secret, time.Now())
	if rr := e.do(http.MethodDelete, "/mfa", access, map[string]string{"code": code}); rr.Code != http.StatusTooManyRequests {
		t.Errorf("after 5 wrong codes, even a right one: %d, want 429", rr.Code)
	}
}

func TestTokenMintedByTokenIsCapped(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	e.user("owner", "operator")
	access := e.login("owner").AccessToken
	parent, parentID := e.apiTokenWith(access, map[string]any{"name": "p", "role": "operator", "expires_in_days": 1})
	_, childID := e.apiTokenWith(parent, map[string]any{"name": "c", "role": "viewer", "expires_in_days": 0})
	p, err := e.h.APITokens.Get(ctx, parentID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.h.APITokens.Get(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt == nil || c.ExpiresAt.After(*p.ExpiresAt) {
		t.Errorf("child expires %v, parent %v: child must not outlive parent", c.ExpiresAt, p.ExpiresAt)
	}

	_, siblingID := e.apiTokenWith(access, map[string]any{"name": "s", "role": "viewer"})
	if rr := e.do(http.MethodDelete, "/settings/api-tokens/"+strconv.FormatInt(siblingID, 10), parent, nil); rr.Code != http.StatusNotFound {
		t.Errorf("token revoking its owner's other token: %d, want 404", rr.Code)
	}
	if rr := e.do(http.MethodDelete, "/settings/api-tokens/"+strconv.FormatInt(parentID, 10), parent, nil); rr.Code != http.StatusNoContent {
		t.Errorf("token revoking itself: %d, want 204", rr.Code)
	}
}

func TestMeShowsEffectiveTokenPermissions(t *testing.T) {
	e := newAuthzEnv(t)
	owner := e.user("owner", "operator")
	token := e.apiToken(e.login("owner").AccessToken, "operator")
	if _, err := e.h.Auth.UpdateUser(context.Background(), owner.ID, "", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	rr := e.do(http.MethodGet, "/me", token, nil)
	var me struct{ Permissions []string }
	if err := json.Unmarshal(rr.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Permissions) == 0 {
		t.Fatal("no permissions listed at all")
	}
	for _, p := range me.Permissions {
		if p == string(rbac.PermStacksDeploy) || p == string(rbac.PermContainersExec) {
			t.Errorf("/me lists %s although the owner is now a viewer", p)
		}
	}
}

// An open WebSocket must end when the session behind its ticket is revoked
// or the caller loses the permission, not only new connections.
func TestOpenWebSocketEndsWithItsSession(t *testing.T) {
	prev := wsRecheckInterval
	wsRecheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { wsRecheckInterval = prev })

	tests := []struct {
		name   string
		revoke func(e *authzEnv, u *auth.User)
	}{
		{"sessions revoked", func(e *authzEnv, u *auth.User) {
			if _, err := e.h.Auth.RevokeUserSessions(context.Background(), u.ID, ""); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"demoted to viewer", func(e *authzEnv, u *auth.User) {
			if _, err := e.h.Auth.UpdateUser(context.Background(), u.ID, "", "viewer", ""); err != nil {
				e.t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthzEnv(t)
			srv := httptest.NewServer(e.router)
			defer srv.Close()
			u := e.user("op", "operator")
			tk := e.ticket(e.login("op").AccessToken, "containers.exec")
			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/test?ticket="+tk, nil)
			if resp != nil {
				resp.Body.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			select {
			case <-e.wsEnded:
				t.Fatal("connection ended before anything was revoked")
			case <-time.After(100 * time.Millisecond):
			}

			tt.revoke(e, u)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err = conn.ReadMessage()
			if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Errorf("after revoking: %v, want policy-violation close", err)
			}
		})
	}
}

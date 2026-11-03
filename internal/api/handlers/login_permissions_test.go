package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Every way of signing in must hand the UI the same permissions as a
// password login, or the sidebar of an SSO user stays empty.
func TestLoginResponsesCarryPermissions(t *testing.T) {
	e := newAuthzEnv(t)
	u := e.user("op", "operator")

	sso := func(t *testing.T) *httptest.ResponseRecorder {
		// OIDC, SAML and OAuth2 callbacks all end in finishSSOLogin.
		login, err := e.h.Auth.StartSessionForSSO(context.Background(), *u, "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		cb := httptest.NewRecorder()
		e.h.finishSSOLogin(cb, httptest.NewRequest(http.MethodGet, "/cb", nil), login)
		code := strings.TrimPrefix(cb.Header().Get("Location"), "/login?sso_code=")
		w := httptest.NewRecorder()
		e.h.SSOExchange(w, ssoExchangeReq(code, code))
		return w
	}
	tests := []struct {
		name string
		do   func(t *testing.T) *httptest.ResponseRecorder
	}{
		{"password", func(t *testing.T) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"op","password":"`+testPassword+`"}`))
			e.h.Login(w, r)
			return w
		}},
		{"sso callback", sso},
		{"refresh", func(t *testing.T) *httptest.ResponseRecorder {
			body, _ := json.Marshal(map[string]string{"refresh_token": e.login("op").RefreshToken})
			w := httptest.NewRecorder()
			e.h.Refresh(w, httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(string(body))))
			return w
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := tt.do(t)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			var res auth.LoginResult
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.User == nil || !slices.Contains(res.User.Permissions, string(rbac.PermStacksOperate)) {
				t.Fatalf("operator signed in without stacks.operate in the response: %+v", res.User)
			}
		})
	}
}

// LDAP answers with writeLogin directly; this pins the shared helper the
// LDAP handler relies on.
func TestWriteLoginAddsPermissions(t *testing.T) {
	e := newAuthzEnv(t)
	u := e.user("ldapuser", "viewer")
	w := httptest.NewRecorder()
	writeLogin(w, http.StatusOK, &auth.LoginResult{AccessToken: "a", User: u})
	var res auth.LoginResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.User == nil || !slices.Contains(res.User.Permissions, string(rbac.PermStacksView)) {
		t.Fatalf("viewer without stacks.view: %+v", res.User)
	}
	if slices.Contains(res.User.Permissions, string(rbac.PermStacksDeploy)) {
		t.Fatal("viewer got stacks.deploy")
	}
}

package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/rbac"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/saml"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// post sends a JSON POST from the given client address straight to a
// handler.
func post(t *testing.T, fn http.HandlerFunc, path, remote string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.RemoteAddr = remote
	rr := httptest.NewRecorder()
	fn(rr, req)
	return rr
}

func enrollTOTP(t *testing.T, e *authzEnv, userID string) string {
	t.Helper()
	enr, err := e.h.Auth.StartTOTPEnrollment(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enr.Secret, time.Now().Add(-30*time.Second))
	if _, err := e.h.Auth.VerifyTOTPEnrollment(context.Background(), userID, code); err != nil {
		t.Fatal(err)
	}
	return enr.Secret
}

func TestTOTPCodeAcceptedOnce(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	u := e.user("carol", "viewer")
	secret := enrollTOTP(t, e, u.ID)

	enrollCode, _ := totp.GenerateCode(secret, time.Now().Add(-30*time.Second))
	if _, err := e.h.Auth.VerifyLoginMFA(ctx, e.login("carol").MFAToken, enrollCode, "test", "127.0.0.1"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("code used for enrollment accepted again: err = %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := e.h.Auth.VerifyLoginMFA(ctx, e.login("carol").MFAToken, code, "test", "127.0.0.1"); err != nil {
		t.Fatalf("fresh code: %v", err)
	}
	if _, err := e.h.Auth.VerifyLoginMFA(ctx, e.login("carol").MFAToken, code, "test", "127.0.0.1"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("replayed code: err = %v, want ErrInvalidCredentials", err)
	}
	if err := e.h.Auth.DisableTOTPWithCode(ctx, u.ID, code); !errors.Is(err, auth.ErrMFAInvalidCode) {
		t.Fatalf("replayed code disables MFA: err = %v", err)
	}
}

func TestLoginMFARateLimitedPerIP(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.mfaLimiter = ratelimit.New(3, time.Minute, time.Minute)
	u := e.user("dave", "viewer")
	secret := enrollTOTP(t, e, u.ID)
	pending := e.login("dave").MFAToken

	for i := range 3 {
		rr := post(t, e.h.LoginMFA, "/auth/mfa", "198.51.100.1:1000", map[string]string{"mfa_token": pending, "code": "000000"})
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i+1, rr.Code)
		}
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	rr := post(t, e.h.LoginMFA, "/auth/mfa", "198.51.100.1:1000", map[string]string{"mfa_token": pending, "code": code})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after 3 wrong codes, a right one: %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}

func TestRefreshRateLimitedPerIP(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.refreshLimits = newAuthLimits(3, 0, time.Minute)
	for i := range 3 {
		rr := post(t, e.h.Refresh, "/auth/refresh", "198.51.100.7:1", map[string]string{"refresh_token": "garbage"})
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i+1, rr.Code)
		}
	}
	e.user("erin", "viewer")
	good := e.login("erin").RefreshToken
	if rr := post(t, e.h.Refresh, "/auth/refresh", "198.51.100.7:1", map[string]string{"refresh_token": good}); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("locked IP: %d, want 429", rr.Code)
	}
	if rr := post(t, e.h.Refresh, "/auth/refresh", "198.51.100.8:1", map[string]string{"refresh_token": good}); rr.Code != http.StatusOK {
		t.Fatalf("other IP: %d %s, want 200", rr.Code, rr.Body)
	}
}

func TestInviteAcceptRateLimited(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Invites = invites.New(e.db, "")
	e.h.inviteLimits = newAuthLimits(3, 100, time.Minute)
	r := chiRouterFor(http.MethodPost, "/invite/{token}/accept", e.h.AcceptInvite)

	send := func(token, remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/invite/"+token+"/accept",
			strings.NewReader(`{"username":"newbie","password":"`+testPassword+`"}`))
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := range 3 {
		if code := send("guess"+strconv.Itoa(i), "198.51.100.9:1"); code != http.StatusNotFound {
			t.Fatalf("guess %d: %d, want 404", i+1, code)
		}
	}
	inviter := e.user("inviter", "admin")
	inv, err := e.h.Invites.Create(context.Background(), invites.CreateInput{Role: "viewer", Scopes: []rbac.Scope{rbac.Global}, CreatedBy: inviter.ID})
	if err != nil {
		t.Fatal(err)
	}
	if code := send(inv.Token, "198.51.100.9:1"); code != http.StatusTooManyRequests {
		t.Fatalf("valid invite from a locked IP: %d, want 429", code)
	}
	if code := send(inv.Token, "198.51.100.10:1"); code != http.StatusCreated {
		t.Fatalf("valid invite from another IP: %d, want 201", code)
	}
}

func TestSSOStateCookieSecure(t *testing.T) {
	e := newAuthzEnv(t)
	store := settings.NewStore(e.db)
	e.h.Settings = store
	trusted := clientip.New(func() string { return "10.0.0.2" })

	tests := []struct {
		name    string
		baseURL string
		remote  string
		proto   string
		tls     bool
		want    bool
	}{
		{name: "plain http", baseURL: "http://dm.lan", remote: "10.0.0.9:1", want: false},
		{name: "https base url", baseURL: "https://dm.example.com", remote: "10.0.0.9:1", want: true},
		{name: "direct tls", remote: "10.0.0.9:1", tls: true, want: true},
		{name: "tls at trusted proxy", remote: "10.0.0.2:1", proto: "https", want: true},
		{name: "forged proto", remote: "10.0.0.9:1", proto: "https", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(context.Background(), settings.KeyBaseURL, tt.baseURL); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/auth/oidc/x/login", nil)
			req.RemoteAddr = tt.remote
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			rr := httptest.NewRecorder()
			trusted.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				e.h.setStateCookie(w, r, oidcStateCookie, "v", 300)
			})).ServeHTTP(rr, req)
			cookies := rr.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies", len(cookies))
			}
			if cookies[0].Secure != tt.want {
				t.Errorf("Secure = %v, want %v", cookies[0].Secure, tt.want)
			}
		})
	}
}

func TestSAMLStateCookie(t *testing.T) {
	e := newAuthzEnv(t)
	store := settings.NewStore(e.db)
	e.h.Settings = store
	e.h.SAML = &saml.Service{}

	tests := []struct {
		name       string
		baseURL    string
		wantSite   http.SameSite
		wantSecure bool
		wantError  string
	}{
		{name: "https", baseURL: "https://dm.example.com", wantSite: http.SameSiteNoneMode, wantSecure: true, wantError: "expired or missing"},
		{name: "plain http", baseURL: "http://dm.lan", wantSite: http.SameSiteLaxMode, wantError: "must be reached over HTTPS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Set(context.Background(), settings.KeyBaseURL, tt.baseURL); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			e.h.setSAMLStateCookie(rr, httptest.NewRequest(http.MethodGet, "/auth/saml/x/login", nil), "v", 300)
			c := rr.Result().Cookies()
			if len(c) != 1 || c[0].SameSite != tt.wantSite || c[0].Secure != tt.wantSecure {
				t.Fatalf("cookie = %+v, want SameSite %v Secure %v", c, tt.wantSite, tt.wantSecure)
			}

			acs := httptest.NewRequest(http.MethodPost, "/auth/saml/x/acs", strings.NewReader("SAMLResponse=x"))
			if tt.wantSecure {
				acs.TLS = &tls.ConnectionState{}
			}
			rr = httptest.NewRecorder()
			e.h.SAMLACS(rr, acs)
			loc, _ := url.QueryUnescape(rr.Header().Get("Location"))
			if rr.Code != http.StatusFound || !strings.HasPrefix(loc, "/login?sso_error=") || !strings.Contains(loc, tt.wantError) {
				t.Errorf("ACS without cookie: %d %q, want redirect mentioning %q", rr.Code, loc, tt.wantError)
			}
		})
	}
}

func TestIdPErrorEscapedInRedirect(t *testing.T) {
	h := &Handlers{OIDC: &oidc.Service{}}
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/x/callback?error="+
		"denied%26access_token%3Devil%23frag%0d%0aX:1", nil)
	rr := httptest.NewRecorder()
	h.OIDCCallback(rr, req)
	loc := rr.Header().Get("Location")
	want := "/login?sso_error=denied%26access_token%3devil%23frag%0d%0aX%3a1"
	if loc != want {
		t.Fatalf("Location = %q, want %q", loc, want)
	}
}

func TestTrustedProxiesSetting(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Settings = settings.NewStore(e.db)
	e.h.proxyNotice = &proxyNotice{notices: e.h.Notices}

	rr := post(t, e.h.UpdateSettings, "/settings", "127.0.0.1:1",
		[]map[string]string{{"key": "base_url", "value": "https://x"}, {"key": settings.KeyTrustedProxies, "value": "10.0.0.0/8, nope"}})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid list: %d, want 400", rr.Code)
	}
	if got := e.h.Settings.Get(settings.KeyBaseURL, ""); got != "" {
		t.Fatalf("a rejected request saved base_url = %q", got)
	}

	e.h.proxyNotice.observe("198.51.100.1")
	waitNotice(t, e, true)

	rr = post(t, e.h.UpdateSettings, "/settings", "127.0.0.1:1",
		[]map[string]string{{"key": settings.KeyTrustedProxies, "value": "10.0.0.0/8"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("valid list: %d %s", rr.Code, rr.Body)
	}
	waitNotice(t, e, false)
	if got := e.h.Settings.Get(settings.KeyTrustedProxies, ""); got != "10.0.0.0/8" {
		t.Fatalf("saved %q", got)
	}
}

func waitNotice(t *testing.T, e *authzEnv, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		open, err := e.h.Notices.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range open {
			if n.Key == noticeKeyUntrustedProxy {
				found = true
			}
		}
		if found == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("trusted proxies notice open = %v, want %v", found, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func chiRouterFor(method, pattern string, fn http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Method(method, pattern, fn)
	return r
}

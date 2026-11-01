package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestProxyRouteSecretsNeedUpdatePermission(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Proxy = proxy.NewService(e.db, nil, false)
	view := middleware.RequirePerm(rbac.PermProxyView)
	e.router.With(middleware.NewAuth(e.h.Auth), view).Get("/proxy/routes", e.h.ListProxyRoutes)
	e.router.With(middleware.NewAuth(e.h.Auth), view).Get("/proxy/routes/{id}/config", e.h.GetProxyRouteConfig)

	opts := proxy.SiteOptions{
		Access: proxy.AccessOptions{ForwardAuth: &proxy.ForwardAuth{URL: "http://authelia:9091/api/verify?token=fa-s3cret"}},
		Headers: proxy.HeaderOptions{RequestSet: []proxy.Header{
			{Name: "X-Plain", Value: "hdr-plain"},
			{Name: "Authorization", Value: "Bearer hdr-s3cret", Secret: true},
		}},
	}
	route, err := e.h.Proxy.CreateRoute(context.Background(), proxy.RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "none", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}

	e.role("proxyviewer", []rbac.Perm{rbac.PermProxyView})
	e.role("proxyeditor", []rbac.Perm{rbac.PermProxyView, rbac.PermProxyUpdate})
	e.user("viewer1", "proxyviewer")
	e.user("editor1", "proxyeditor")
	e.user("admin1", "admin")
	viewerTok := e.login("viewer1").AccessToken
	editorTok := e.login("editor1").AccessToken
	adminTok := e.login("admin1").AccessToken
	viewerAPI := e.apiToken(adminTok, "viewer")

	tests := []struct {
		name, bearer string
		full         bool
	}{
		{"view only", viewerTok, false},
		{"admin token pinned to viewer", viewerAPI, false},
		{"view and update", editorTok, true},
		{"admin", adminTok, true},
	}
	for _, tt := range tests {
		for _, path := range []string{"/proxy/routes", "/proxy/routes/" + strconv.FormatInt(route.ID, 10) + "/config"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				rr := e.do(http.MethodGet, path, tt.bearer, nil)
				if rr.Code != http.StatusOK {
					t.Fatalf("%d %s", rr.Code, rr.Body)
				}
				body := rr.Body.String()
				if !strings.Contains(body, "Authorization") || !strings.Contains(body, "authelia:9091") {
					t.Fatalf("header name or auth host missing: %s", body)
				}
				if got := strings.Contains(body, "hdr-plain"); got != tt.full {
					t.Errorf("plain header value returned = %v, want %v", got, tt.full)
				}
				for _, secret := range []string{"hdr-s3cret", "fa-s3cret"} {
					if strings.Contains(body, secret) {
						t.Errorf("%s returned to %s", secret, tt.name)
					}
				}
			})
		}
	}
}

// An editor's client sends back what it got: write-only values stay as
// stored, and the audit entry names the groups that changed.
func TestProxyRouteOptionsRoundTrip(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Proxy = proxy.NewService(e.db, nil, false)
	e.h.Audit = audit.NewService(e.db, "")
	auth := middleware.NewAuth(e.h.Auth)
	e.router.With(auth, middleware.RequirePerm(rbac.PermProxyView)).Get("/proxy/routes", e.h.ListProxyRoutes)
	e.router.With(auth, middleware.RequirePerm(rbac.PermProxyUpdate)).Put("/proxy/routes/{id}", e.h.UpdateProxyRoute)

	opts := proxy.SiteOptions{
		Access: proxy.AccessOptions{
			BasicAuth:   []proxy.BasicAuthUser{{Username: "u1", Password: "password-1"}},
			ForwardAuth: &proxy.ForwardAuth{URL: "http://authelia:9091/api/verify?token=fa-s3cret"},
		},
		Headers: proxy.HeaderOptions{RequestSet: []proxy.Header{{Name: "Authorization", Value: "Bearer hdr-s3cret", Secret: true}}},
	}
	route, err := e.h.Proxy.CreateRoute(context.Background(), proxy.RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	e.role("proxyeditor", []rbac.Perm{rbac.PermProxyView, rbac.PermProxyUpdate})
	e.user("editor1", "proxyeditor")
	tok := e.login("editor1").AccessToken
	path := "/proxy/routes/" + strconv.FormatInt(route.ID, 10)

	rr := e.do(http.MethodGet, "/proxy/routes", tok, nil)
	var list []struct {
		Upstream string          `json:"upstream"`
		TLSMode  string          `json:"tls_mode"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %s", err, rr.Body)
	}
	got := list[0]

	steps := []struct {
		name    string
		options json.RawMessage
		code    int
		changed []string
	}{
		{"unchanged", got.Options, http.StatusOK, []string{}},
		{"logs on", json.RawMessage(strings.Replace(string(got.Options), `"access_log":false`, `"access_log":true`, 1)), http.StatusOK, []string{"options.logs"}},
		{"bad timeout", json.RawMessage(`{"performance":{"read_timeout":"forever"}}`), http.StatusBadRequest, nil},
	}
	for _, st := range steps {
		rr := e.do(http.MethodPut, path, tok, map[string]any{"upstream": got.Upstream, "tls_mode": got.TLSMode, "options": st.options})
		if rr.Code != st.code {
			t.Fatalf("%s: %d %s", st.name, rr.Code, rr.Body)
		}
		if st.code != http.StatusOK {
			if !strings.Contains(rr.Body.String(), "options.performance.read_timeout") {
				t.Errorf("%s: error does not name the field: %s", st.name, rr.Body)
			}
			continue
		}
		var details string
		if err := e.db.QueryRow(`SELECT details FROM audit_log WHERE action = 'proxy.route_update' ORDER BY id DESC LIMIT 1`).Scan(&details); err != nil {
			t.Fatal(err)
		}
		var d struct{ Changed []string }
		if err := json.Unmarshal([]byte(details), &d); err != nil {
			t.Fatal(err)
		}
		if strings.Join(d.Changed, ",") != strings.Join(st.changed, ",") {
			t.Errorf("%s: audit changed = %v, want %v", st.name, d.Changed, st.changed)
		}
		for _, secret := range []string{"hdr-s3cret", "fa-s3cret", "password-1"} {
			if strings.Contains(details, secret) || strings.Contains(rr.Body.String(), secret) {
				t.Errorf("%s: %s leaked", st.name, secret)
			}
		}
	}

	stored, err := e.h.Proxy.SiteConfig(context.Background(), route.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Blocked {
		t.Fatal("round-tripped route is blocked")
	}
	var raw string
	if err := e.db.QueryRow(`SELECT options FROM proxy_routes WHERE id = ?`, route.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Bearer hdr-s3cret", "token=fa-s3cret", `"hash":"$2`} {
		if !strings.Contains(raw, want) {
			t.Errorf("stored options lost %s: %s", want, raw)
		}
	}
}

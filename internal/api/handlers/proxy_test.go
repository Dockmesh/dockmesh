package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
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
		Access:  proxy.AccessOptions{ForwardAuth: &proxy.ForwardAuth{URL: "http://authelia:9091/api/verify?token=fa-s3cret"}},
		Headers: proxy.HeaderOptions{RequestSet: []proxy.Header{{Name: "Authorization", Value: "Bearer hdr-s3cret"}}},
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
				for _, secret := range []string{"hdr-s3cret", "fa-s3cret"} {
					if got := strings.Contains(body, secret); got != tt.full {
						t.Errorf("%s returned = %v, want %v", secret, got, tt.full)
					}
				}
			})
		}
	}
}

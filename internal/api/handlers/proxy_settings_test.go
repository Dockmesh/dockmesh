package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestProxySettingsEndpoints(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Settings = settings.NewStore(e.db)
	e.h.Proxy = proxy.NewService(e.db, nil, false)
	e.h.Proxy.SetSettings(e.h.Settings)

	rr := post(t, e.h.UpdateProxySettings, "/proxy/settings", "127.0.0.1:1", map[string]string{"acme_ca": "zerossl"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("zerossl without email: %d", rr.Code)
	}
	rr = post(t, e.h.UpdateProxySettings, "/proxy/settings", "127.0.0.1:1",
		map[string]string{"acme_email": "ops@example.com", "acme_ca": "zerossl", "trusted_proxies": "cloudflare"})
	if rr.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", rr.Code, rr.Body)
	}
	var got proxy.Settings
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (proxy.Settings{ACMEEmail: "ops@example.com", ACMECA: "zerossl", TrustedProxies: "cloudflare"}) {
		t.Fatalf("settings = %+v", got)
	}

	// The generic settings API refuses to change proxy keys but accepts
	// them unchanged, as the settings page sends every key back.
	rr = post(t, e.h.UpdateSettings, "/settings", "127.0.0.1:1",
		[]map[string]string{{"key": proxy.KeyACMECA, "value": "letsencrypt_staging"}})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("generic change of a proxy key: %d", rr.Code)
	}
	rr = post(t, e.h.UpdateSettings, "/settings", "127.0.0.1:1",
		[]map[string]string{{"key": proxy.KeyACMECA, "value": "zerossl"}, {"key": settings.KeyBaseURL, "value": "https://x"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("unchanged proxy key: %d %s", rr.Code, rr.Body)
	}
	rr = post(t, e.h.UpdateSettings, "/settings", "127.0.0.1:1",
		[]map[string]string{{"key": proxy.KeyCaddyVersion, "value": "2.11.6"}})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("generic version change: %d", rr.Code)
	}

	rr = post(t, e.h.SetProxyVersion, "/proxy/version", "127.0.0.1:1", map[string]string{"version": "1.0.0"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown version: %d", rr.Code)
	}
	rr = post(t, e.h.RollbackProxyVersion, "/proxy/version/rollback", "127.0.0.1:1", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("rollback without previous: %d", rr.Code)
	}

	readOnly := proxy.NewService(e.db, nil, false)
	readOnly.SetImage("registry.example/caddy:custom")
	e.h.Proxy = readOnly
	rr = post(t, e.h.SetProxyVersion, "/proxy/version", "127.0.0.1:1", map[string]string{"version": proxy.TestedVersions[0].Version})
	if rr.Code != http.StatusConflict {
		t.Fatalf("switch with DOCKMESH_PROXY_IMAGE: %d", rr.Code)
	}
}

func TestProxySettingsViewersSeeMaskedValues(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Settings = settings.NewStore(e.db)
	e.h.Proxy = proxy.NewService(e.db, nil, false)
	e.h.Proxy.SetImage("registry.internal.example/caddy:custom")
	e.h.Proxy.SetSettings(e.h.Settings)
	if err := e.h.Settings.Set(context.Background(), proxy.KeyACMEEmail, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	view := middleware.RequirePerm(rbac.PermProxyView)
	e.router.With(middleware.NewAuth(e.h.Auth), view).Get("/proxy/settings", e.h.GetProxySettings)
	e.router.With(middleware.NewAuth(e.h.Auth), view).Get("/proxy/version", e.h.GetProxyVersion)
	e.role("proxyviewer", []rbac.Perm{rbac.PermProxyView})
	e.user("viewer1", "proxyviewer")
	e.user("admin1", "admin")

	tests := []struct {
		user string
		full bool
	}{
		{"viewer1", false},
		{"admin1", true},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			tok := e.login(tt.user).AccessToken
			rr := e.do(http.MethodGet, "/proxy/settings", tok, nil)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "@example.com") {
				t.Fatalf("settings: %d %s", rr.Code, rr.Body)
			}
			if got := strings.Contains(rr.Body.String(), "ops@"); got != tt.full {
				t.Errorf("email local part shown = %v, want %v", got, tt.full)
			}
			rr = e.do(http.MethodGet, "/proxy/version", tok, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("version: %d %s", rr.Code, rr.Body)
			}
			if got := strings.Contains(rr.Body.String(), "registry.internal"); got != tt.full {
				t.Errorf("custom image shown = %v, want %v", got, tt.full)
			}
		})
	}
}

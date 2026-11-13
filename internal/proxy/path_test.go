package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckURLPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"plain", "/health", ""},
		{"nested", "/api/v1/status", ""},
		{"escaped space", "/a%20b", ""},
		{"escaped slash", "/a%2Fb", ""},
		{"lower-case escape", "/caf%c3%a9", ""},
		{"non-ascii", "/über", ""},
		{"double slash", "//x", ""},
		{"trailing slash", "/ords/", ""},
		{"dots inside a segment", "/v1.2/..x/a..", ""},
		// Caddy 2.11.7 refuses these when it provisions the config.
		{"invalid escape", "/%zz", "two hex digits"},
		{"short escape", "/%2", "two hex digits"},
		{"bare percent", "/%", "two hex digits"},
		{"percent at the end", "/a%", "two hex digits"},
		{"tab", "/a\tb", "control"},
		{"delete", "/a\x7fb", "control"},
		{"nul", "/a\x00b", "control"},
		// Caddy loads these, but they are not a path Dockmesh writes.
		{"space", "/a b", "spaces"},
		{"fragment", "/a#x", "spaces, # or ?"},
		{"query", "/a?x=1", "spaces, # or ?"},
		{"placeholder", "/{env.SECRET}", "braces"},
		{"dot-dot at start", "/../x", ". or .."},
		{"dot-dot inside", "/a/../b", ". or .."},
		{"dot-dot at end", "/a/..", ". or .."},
		{"dot segment", "/a/./b", ". or .."},
		{"relative", "health", "start with /"},
		{"empty", "", "empty"},
		{"too long", "/" + strings.Repeat("a", 256), "longer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkURLPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkURLPath(%q) = %v", tt.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkURLPath(%q) = %v, want containing %q", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestCheckChangedPaths(t *testing.T) {
	health := func(p string) *SiteOptions { return &SiteOptions{Routing: RoutingOptions{HealthPath: p}} }
	fwd := func(u string) *SiteOptions {
		return &SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: u}}}
	}
	tests := []struct {
		name      string
		o, stored *SiteOptions
		field     string
	}{
		{"new site, good health path", health("/health"), nil, ""},
		{"new site, health path with query", health("/health?full=1"), nil, ""},
		{"new site, invalid escape", health("/%zz"), nil, "routing.health_path"},
		{"new site, dot-dot", health("/a/../b"), nil, "routing.health_path"},
		{"saved invalid path kept", health("/%zz"), health("/%zz"), ""},
		{"saved dot-dot path kept", health("/a/../b"), health("/a/../b"), ""},
		{"saved path changed to another bad one", health("/%zy"), health("/%zz"), "routing.health_path"},
		{"saved bad path fixed", health("/health"), health("/%zz"), ""},
		{"health path cleared", health(""), health("/%zz"), ""},
		{"forward auth without path", fwd("http://auth:9091"), nil, ""},
		{"forward auth good path", fwd("http://auth:9091/api/verify?rd=x"), nil, ""},
		{"forward auth dot-dot", fwd("http://auth:9091/api/../verify"), nil, "access.forward_auth.url"},
		{"forward auth saved dot-dot kept", fwd("http://auth:9091/api/../verify"), fwd("http://auth:9091/api/../verify?t=1"), ""},
		{"forward auth dot-dot new on a saved site", fwd("http://auth:9091/a/../b"), fwd("http://auth:9091/a"), "access.forward_auth.url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.o.checkChangedPaths(tt.stored)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var oe *OptionError
			if !errors.As(err, &oe) || oe.Field != tt.field || !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("err = %v, want field %s", err, tt.field)
			}
		})
	}
}

// A health path saved before the stricter check keeps loading and keeps
// saving unchanged; only a changed value is refused.
func TestSavedHealthPathSurvivesUpgrade(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "b.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto",
		Options: &SiteOptions{Routing: RoutingOptions{HealthPath: "/a/../health"}}}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("new site with dot-dot health path: err = %v", err)
	}
	if _, err := s.db.Exec(`UPDATE proxy_routes SET options = '{"routing":{"health_path":"/a/../health"}}' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}

	routes, err := s.listRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, blocked := buildConfig(routes, s.ProxySettings()); len(blocked) != 0 {
		t.Fatalf("saved site left out of the config: %v", blocked)
	}
	if err := s.SetRouteEnabled(ctx, r.ID, true); err != nil {
		t.Fatalf("enable with saved path: %v", err)
	}
	kept := SiteOptions{Routing: RoutingOptions{HealthPath: "/a/../health"}, Performance: PerformanceOptions{Compression: true}}
	if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, &kept); err != nil {
		t.Fatalf("save with the health path unchanged: %v", err)
	}
	if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, nil); err != nil {
		t.Fatalf("save without options: %v", err)
	}
	changed := SiteOptions{Routing: RoutingOptions{HealthPath: "/b/../health"}}
	var oe *OptionError
	if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, &changed); !errors.As(err, &oe) || oe.Field != "routing.health_path" {
		t.Fatalf("changed health path: err = %v", err)
	}
}

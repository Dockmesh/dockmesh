package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"golang.org/x/crypto/bcrypt"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// testHash is bcrypt("correct horse") at the minimum cost.
const testHash = "$2a$04$LJ3FMD0CUmp0L58htuEqR.j9XBgCTxlHttAycaIOV10mIFLQNOS2y"

func site(tls string, o SiteOptions) Route {
	return Route{Host: "app.example.com", Upstream: "10.0.0.5:8080", TLSMode: tls, Enabled: true, Options: o}
}

func TestBuildConfigGolden(t *testing.T) {
	tests := []struct {
		name  string
		route Route
	}{
		{"defaults", site("auto", SiteOptions{})},
		{"defaults_none", site("none", SiteOptions{})},
		{"flush", site("auto", SiteOptions{Performance: PerformanceOptions{FlushImmediately: true}})},
		{"buffer_requests", site("auto", SiteOptions{Performance: PerformanceOptions{BufferRequests: true}})},
		{"timeouts", site("auto", SiteOptions{Performance: PerformanceOptions{DialTimeout: "30s", ReadTimeout: "1h", WriteTimeout: "1h"}})},
		{"max_body", site("auto", SiteOptions{Performance: PerformanceOptions{MaxBodyBytes: 10 << 30}})},
		{"stream_close_delay", site("auto", SiteOptions{Performance: PerformanceOptions{StreamCloseDelay: "5m"}})},
		{"compression", site("auto", SiteOptions{Performance: PerformanceOptions{Compression: true}})},
		{"load_balancing", site("auto", SiteOptions{Routing: RoutingOptions{
			ExtraUpstreams: []string{"10.0.0.6:8080"}, LBPolicy: "least_conn", HealthPath: "/health"}})},
		{"www_redirect", site("auto", SiteOptions{Routing: RoutingOptions{WWWRedirect: true}})},
		{"www_redirect_none", site("none", SiteOptions{Routing: RoutingOptions{WWWRedirect: true}})},
		{"internal_ca", site("internal", SiteOptions{})},
		{"allow_http", site("auto", SiteOptions{HTTPS: HTTPSOptions{AllowHTTP: true}})},
		{"hsts", site("auto", SiteOptions{HTTPS: HTTPSOptions{HSTS: true}})},
		{"basic_auth", site("auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "marcel", Hash: testHash}}}})},
		{"ip_allow", site("auto", SiteOptions{Access: AccessOptions{IPMode: "allow", IPRanges: []string{"192.168.10.0/24", "10.8.0.1/32"}}})},
		{"ip_deny", site("auto", SiteOptions{Access: AccessOptions{IPMode: "deny", IPRanges: []string{"203.0.113.0/24"}}})},
		{"forward_auth", site("auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{
			URL: "http://authelia:9091/api/verify?rd=https://auth.example.org", CopyHeaders: []string{"Remote-User", "Remote-Groups"}}}})},
		{"forward_auth_https", site("auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "https://auth.example.org/verify"}}})},
		{"security_headers", site("auto", SiteOptions{Headers: HeaderOptions{Security: true}})},
		{"custom_headers", site("auto", SiteOptions{Headers: HeaderOptions{
			RequestSet: []Header{{Name: "X-Forwarded-Proto", Value: "https"}}, RequestRemove: []string{"X-Debug"},
			ResponseSet: []Header{{Name: "X-Robots-Tag", Value: "noindex"}}, ResponseRemove: []string{"Server"}}})},
		{"access_log", site("auto", SiteOptions{Logs: LogOptions{AccessLog: true}})},
		{"access_log_none", site("none", SiteOptions{Logs: LogOptions{AccessLog: true}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.route.Options.validate(tt.route.Host, tt.route.TLSMode); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			cfg, blocked := buildConfig([]Route{tt.route})
			if len(blocked) > 0 {
				t.Fatalf("blocked = %v", blocked)
			}
			got, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "config", tt.name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
				t.Errorf("config differs from %s:\n%s", path, got)
			}
		})
	}
}

// A route stored before options existed must produce byte-for-byte the
// config Dockmesh pushed before, so an upgrade doesn't reload Caddy.
func TestBuildConfigDefaultsMatchLegacyOutput(t *testing.T) {
	stored, err := decodeOptions([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := buildConfig([]Route{
		{Host: "a.example.com", Upstream: "10.0.0.5:8080", TLSMode: "auto", Enabled: true, Options: stored},
		{Host: "b.lan", Upstream: "10.0.0.6:80", TLSMode: "none", Enabled: true, Options: stored},
	})
	got, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const legacy = `{"admin":{"listen":"127.0.0.1:2019","origins":["127.0.0.1:2019","localhost:2019"],"enforce_origin":true},` +
		`"apps":{"http":{"servers":{` +
		`"dockmesh":{"listen":[":443"],"routes":[{"match":[{"host":["a.example.com"]}],"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"10.0.0.5:8080"}]}],"terminal":true}]},` +
		`"dockmesh_http":{"listen":[":80"],"routes":[{"match":[{"host":["b.lan"]}],"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"10.0.0.6:80"}]}],"terminal":true}]}}},` +
		`"tls":{"automation":{"policies":[{"subjects":["a.example.com"],"issuers":[{"module":"acme"}]}]}}}}`
	if string(got) != legacy {
		t.Fatalf("defaults changed the generated config:\n got %s\nwant %s", got, legacy)
	}
}

func TestBuildConfigWWWRedirectYieldsToRealSite(t *testing.T) {
	cfg, _ := buildConfig([]Route{
		site("auto", SiteOptions{Routing: RoutingOptions{WWWRedirect: true}}),
		{Host: "www.app.example.com", Upstream: "10.0.0.9:80", TLSMode: "auto", Enabled: true},
	})
	for _, p := range cfg.Apps.TLS.Automation.Policies {
		if p.Subjects[0] == "app.example.com" && len(p.Subjects) != 1 {
			t.Fatalf("www name claimed by the redirect: %v", p.Subjects)
		}
	}
}

func TestBuildConfigBlocksInvalidStoredOptions(t *testing.T) {
	bad := site("auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "x"}}}})
	unreadable := Route{Host: "b.example.com", Upstream: "10.0.0.5:80", TLSMode: "auto", Enabled: true, optionsErr: errors.New("bad json")}
	_, blocked := buildConfig([]Route{bad, unreadable})
	if len(blocked) != 2 {
		t.Fatalf("blocked = %v", blocked)
	}
}

func TestSiteOptionsValidate(t *testing.T) {
	tests := []struct {
		name  string
		tls   string
		o     SiteOptions
		field string
	}{
		{"defaults", "auto", SiteOptions{}, ""},
		{"bad extra upstream", "auto", SiteOptions{Routing: RoutingOptions{ExtraUpstreams: []string{"localhost:2019"}}}, "routing.extra_upstreams"},
		{"duplicate upstream", "auto", SiteOptions{Routing: RoutingOptions{ExtraUpstreams: []string{"a:1", "a:1"}}}, "routing.extra_upstreams"},
		{"bad lb policy", "auto", SiteOptions{Routing: RoutingOptions{LBPolicy: "cookie"}}, "routing.lb_policy"},
		{"health path without slash", "auto", SiteOptions{Routing: RoutingOptions{HealthPath: "health"}}, "routing.health_path"},
		{"health path placeholder", "auto", SiteOptions{Routing: RoutingOptions{HealthPath: "/{env.X}"}}, "routing.health_path"},
		{"hsts without tls", "none", SiteOptions{HTTPS: HTTPSOptions{HSTS: true}}, "https.hsts"},
		{"timeout garbage", "auto", SiteOptions{Performance: PerformanceOptions{ReadTimeout: "forever"}}, "performance.read_timeout"},
		{"timeout too long", "auto", SiteOptions{Performance: PerformanceOptions{WriteTimeout: "200h"}}, "performance.write_timeout"},
		{"timeout too short", "auto", SiteOptions{Performance: PerformanceOptions{DialTimeout: "10ms"}}, "performance.dial_timeout"},
		{"negative body", "auto", SiteOptions{Performance: PerformanceOptions{MaxBodyBytes: -1}}, "performance.max_body_bytes"},
		{"user without hash", "auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "a"}}}}, "access.basic_auth"},
		{"user with colon", "auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "a:b", Hash: testHash}}}}, "access.basic_auth"},
		{"duplicate user", "auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "a", Hash: testHash}, {Username: "a", Hash: testHash}}}}, "access.basic_auth"},
		{"bad ip mode", "auto", SiteOptions{Access: AccessOptions{IPMode: "only"}}, "access.ip_mode"},
		{"allow without ranges", "auto", SiteOptions{Access: AccessOptions{IPMode: "allow"}}, "access.ip_ranges"},
		{"bad range", "auto", SiteOptions{Access: AccessOptions{IPMode: "deny", IPRanges: []string{"10.0.0.0/33"}}}, "access.ip_ranges"},
		{"forward auth bad scheme", "auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "ftp://auth:21/"}}}, "access.forward_auth.url"},
		{"forward auth admin api", "auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://127.0.0.1:2019/config/"}}}, "access.forward_auth.url"},
		{"forward auth placeholder", "auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://auth:9091/{file./data/x}"}}}, "access.forward_auth.url"},
		{"forward auth bad header", "auto", SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://auth:9091/", CopyHeaders: []string{"Bad Header"}}}}, "access.forward_auth.copy_headers"},
		{"header placeholder", "auto", SiteOptions{Headers: HeaderOptions{ResponseSet: []Header{{Name: "X-Leak", Value: "{env.JWT_SECRET}"}}}}, "headers.response_set"},
		{"header newline", "auto", SiteOptions{Headers: HeaderOptions{RequestSet: []Header{{Name: "X-A", Value: "a\r\nX-B: b"}}}}, "headers.request_set"},
		{"header bad name", "auto", SiteOptions{Headers: HeaderOptions{RequestRemove: []string{"X:A"}}}, "headers.request_remove"},
		{"www redirect", "auto", SiteOptions{Routing: RoutingOptions{WWWRedirect: true}}, ""},
		{"hash with placeholder", "auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "a", Hash: "$2a$10${file./config/caddy/autosave.json}AAAAAAAAAAAAAAAAAAAAAAAAAA"}}}}, "access.basic_auth"},
		{"hash cost too high", "auto", SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "a", Hash: "$2a$31$" + testHash[7:]}}}}, "access.basic_auth"},
		{"wildcard header delete", "auto", SiteOptions{Headers: HeaderOptions{ResponseRemove: []string{"*"}}}, "headers.response_remove"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.o.validate("app.example.com", tt.tls)
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
	www := SiteOptions{Routing: RoutingOptions{WWWRedirect: true}}
	for _, host := range []string{"www.example.com", "*.example.com", "192.0.2.4"} {
		if err := www.validate(host, "auto"); err == nil {
			t.Errorf("www redirect on %s accepted", host)
		}
	}
}

func TestApplyPasswords(t *testing.T) {
	prev := SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "kept", Hash: testHash}}}}
	o := SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{
		{Username: "kept"},
		{Username: "new", Password: "a new password", Hash: "$2a$04$attacker-chosen"},
	}}}
	if err := o.applyPasswords(&prev); err != nil {
		t.Fatal(err)
	}
	if o.Access.BasicAuth[0].Hash != testHash {
		t.Fatal("existing hash not kept")
	}
	nu := o.Access.BasicAuth[1]
	if nu.Password != "" || bcrypt.CompareHashAndPassword([]byte(nu.Hash), []byte("a new password")) != nil {
		t.Fatalf("new user not hashed: %+v", nu)
	}
	for _, bad := range []BasicAuthUser{{Username: "nobody"}, {Username: "short", Password: "1234567"}, {Username: "long", Password: strings.Repeat("x", 73)}} {
		o := SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{bad}}}
		if err := o.applyPasswords(&prev); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: err = %v", bad.Username, err)
		}
	}
	if pub := prev.Public(); pub.Access.BasicAuth[0].Hash != "" || pub.Access.BasicAuth[0].Username != "kept" {
		t.Fatalf("Public leaks hash: %+v", pub.Access.BasicAuth)
	}
	if prev.Access.BasicAuth[0].Hash != testHash {
		t.Fatal("Public modified the original")
	}
}

func newServiceDB(t *testing.T) *Service {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "dockmesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	return NewService(conn, nil, false)
}

func TestRouteOptionsLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)

	plain, err := s.CreateRoute(ctx, RouteSpec{Host: "plain.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT options FROM proxy_routes WHERE id = ?`, plain.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if o, _ := decodeOptions([]byte(raw)); !jsonEqual(t, o, SiteOptions{}) {
		t.Fatalf("new route without options stored %s", raw)
	}

	opts := SiteOptions{
		Access:      AccessOptions{BasicAuth: []BasicAuthUser{{Username: "marcel", Password: "correct horse"}}, IPMode: "allow", IPRanges: []string{"192.168.10.7", "192.168.10.7/32"}},
		Performance: PerformanceOptions{FlushImmediately: true},
	}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	if r.Options.Access.BasicAuth[0].Hash != "" {
		t.Fatal("create returned a hash")
	}
	stored, err := s.getRoute(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := stored.Options.Access.BasicAuth[0].Hash
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse")) != nil {
		t.Fatal("stored hash doesn't match the password")
	}
	if got := stored.Options.Access.IPRanges; len(got) != 1 || got[0] != "192.168.10.7/32" {
		t.Fatalf("ip ranges not normalised: %v", got)
	}

	// Updating without options keeps them; resending a user without a
	// password keeps its hash.
	if err := s.UpdateRoute(ctx, r.ID, "10.0.0.3:80", "auto", true, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.getRoute(ctx, r.ID); got.Options.Access.BasicAuth[0].Hash != hash || !got.Options.Performance.FlushImmediately {
		t.Fatalf("options lost on update without options: %+v", got.Options)
	}
	keep := SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "marcel"}}}}
	if err := s.UpdateRoute(ctx, r.ID, "10.0.0.3:80", "auto", true, &keep); err != nil {
		t.Fatal(err)
	}
	got, _ := s.getRoute(ctx, r.ID)
	if got.Options.Access.BasicAuth[0].Hash != hash || got.Options.Performance.FlushImmediately || got.Options.Access.IPMode != "" {
		t.Fatalf("options not replaced: %+v", got.Options)
	}

	hsts := SiteOptions{HTTPS: HTTPSOptions{HSTS: true}}
	if err := s.UpdateRoute(ctx, r.ID, "10.0.0.3:80", "none", true, &hsts); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("hsts on tls none: err = %v", err)
	}
	if err := s.UpdateRoute(ctx, 999, "10.0.0.3:80", "auto", true, nil); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("missing route: err = %v", err)
	}

	list, err := s.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range list {
		for _, u := range rt.Options.Access.BasicAuth {
			if u.Hash != "" {
				t.Fatal("list returned a hash")
			}
		}
	}

	preview, err := s.SiteConfig(ctx, r.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(preview)
	if strings.Contains(string(b), hash) || !strings.Contains(string(b), redactedHash) || strings.Contains(string(b), adminAddr) {
		t.Fatalf("preview leaks the hash or admin section: %s", b)
	}
	if _, err := s.SiteConfig(ctx, 999, false); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("preview of missing route: err = %v", err)
	}
}

func TestWWWRedirectConflicts(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	www := &SiteOptions{Routing: RoutingOptions{WWWRedirect: true}}
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "www.a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto", Options: www}); !errors.Is(err, ErrWWWConflict) {
		t.Fatalf("redirect onto an existing site: err = %v", err)
	}
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "b.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto", Options: www}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "www.b.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"}); !errors.Is(err, ErrWWWConflict) {
		t.Fatalf("site on a redirected name: err = %v", err)
	}
}

func TestSetRouteEnabledRefusesInvalidStoredOptions(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE proxy_routes SET enabled = 0, options = '{"access":{"ip_mode":"allow"}}' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRouteEnabled(ctx, r.ID, true); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("err = %v", err)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(x, y)
}

func TestMaskedHidesCredentials(t *testing.T) {
	o := SiteOptions{
		Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://authelia:9091/api/verify?token=s3cret", CopyHeaders: []string{"Remote-User"}}},
		Headers: HeaderOptions{
			RequestSet:    []Header{{Name: "Authorization", Value: "Bearer s3cret"}},
			ResponseSet:   []Header{{Name: "X-Frame-Options", Value: "DENY"}},
			RequestRemove: []string{"Cookie"},
		},
	}
	m := o.Masked()
	if got := m.Access.ForwardAuth.URL; got != "http://authelia:9091/api/verify" {
		t.Errorf("forward-auth URL = %q", got)
	}
	if m.Access.ForwardAuth.CopyHeaders[0] != "Remote-User" || m.Headers.RequestRemove[0] != "Cookie" {
		t.Errorf("names lost: %+v", m)
	}
	for _, h := range append(m.Headers.RequestSet, m.Headers.ResponseSet...) {
		if h.Value != maskedValue {
			t.Errorf("%s = %q, want masked", h.Name, h.Value)
		}
	}
	if m.Headers.RequestSet[0].Name != "Authorization" {
		t.Errorf("header name lost: %+v", m.Headers.RequestSet)
	}
	if o.Headers.RequestSet[0].Value != "Bearer s3cret" || !strings.Contains(o.Access.ForwardAuth.URL, "token=") {
		t.Fatal("Masked modified the original")
	}
	if (SiteOptions{}).Masked().Access.ForwardAuth != nil {
		t.Error("Masked added a forward-auth block")
	}
}

func TestSiteConfigMasked(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	opts := SiteOptions{
		Access:  AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://authelia:9091/api/verify?token=s3cret"}},
		Headers: HeaderOptions{RequestSet: []Header{{Name: "Authorization", Value: "Bearer s3cret"}}},
	}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []bool{false, true} {
		preview, err := s.SiteConfig(ctx, r.ID, mask)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(preview)
		if leaked := strings.Contains(string(b), "s3cret"); leaked == mask {
			t.Errorf("mask=%v: secret in preview = %v: %s", mask, leaked, b)
		}
		if mask && (!strings.Contains(string(b), maskedValue) || !strings.Contains(string(b), "/api/verify")) {
			t.Errorf("masked preview lost the header or the auth path: %s", b)
		}
	}
}

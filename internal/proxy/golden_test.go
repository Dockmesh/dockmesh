package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// allOptions sets every option group at once, so a change in how groups
// combine shows up in the golden files.
func allOptions() SiteOptions {
	return SiteOptions{
		Routing: RoutingOptions{ExtraUpstreams: []string{"10.0.0.6:8080"}, LBPolicy: "round_robin", HealthPath: "/healthz", WWWRedirect: true},
		HTTPS:   HTTPSOptions{AllowHTTP: true, HSTS: true},
		Performance: PerformanceOptions{
			FlushImmediately: true, BufferRequests: true, DialTimeout: "5s", ReadTimeout: "2m", WriteTimeout: "2m",
			MaxBodyBytes: 1 << 20, StreamCloseDelay: "30s", Compression: true,
		},
		Access: AccessOptions{
			BasicAuth: []BasicAuthUser{{Username: "ops", Hash: testHash}},
			IPMode:    "allow", IPRanges: []string{"192.168.10.0/24"},
			ForwardAuth: &ForwardAuth{URL: "http://authelia:9091/api/verify?rd=https://auth.example.org", CopyHeaders: []string{"Remote-User"}},
		},
		Headers: HeaderOptions{
			Security:   true,
			RequestSet: []Header{{Name: "X-Env", Value: "lab"}, {Name: "X-Api-Key", Value: "s3cret", Secret: true}}, RequestRemove: []string{"X-Debug"},
			ResponseSet: []Header{{Name: "Cache-Control", Value: "no-store"}}, ResponseRemove: []string{"Server"},
		},
		Logs: LogOptions{AccessLog: true},
	}
}

// goldenSites is a set of sites as an install has them: without options,
// every TLS mode, every option group on one site, a disabled site and a
// www redirect that yields to a real www site.
func goldenSites() []Route {
	return []Route{
		{Host: "plain.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto", Enabled: true},
		{Host: "lan.home", Upstream: "10.0.0.2:80", TLSMode: "none", Enabled: true},
		{Host: "internal.home", Upstream: "10.0.0.3:443", TLSMode: "internal", Enabled: true,
			Options: SiteOptions{Performance: PerformanceOptions{Compression: true}, Logs: LogOptions{AccessLog: true}}},
		{Host: "full.example.com", Upstream: "10.0.0.5:8080", TLSMode: "auto", Enabled: true, Options: allOptions()},
		{Host: "lanfull.home", Upstream: "10.0.0.7:80", TLSMode: "none", Enabled: true,
			Options: SiteOptions{Routing: RoutingOptions{WWWRedirect: true}, Access: AccessOptions{IPMode: "deny", IPRanges: []string{"203.0.113.0/24"}},
				Headers: HeaderOptions{Security: true}, Logs: LogOptions{AccessLog: true}}},
		{Host: "off.example.com", Upstream: "10.0.0.8:80", TLSMode: "auto", Enabled: false, Options: allOptions()},
		{Host: "shop.example.com", Upstream: "10.0.0.9:80", TLSMode: "auto", Enabled: true,
			Options: SiteOptions{Routing: RoutingOptions{WWWRedirect: true}}},
		{Host: "www.shop.example.com", Upstream: "10.0.0.10:80", TLSMode: "auto", Enabled: true},
	}
}

// The generated config for a whole install is pinned byte for byte:
// Caddy reloads (and may reissue certificates) on any difference, so an
// upgrade must not change it for sites whose settings didn't change.
func TestBuildConfigGoldenSites(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
	}{
		{"sites", Settings{}},
		{"sites_settings", Settings{ACMEEmail: "ops@example.com", ACMECA: "zerossl", TrustedProxies: "cloudflare"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := goldenSites()
			for _, r := range routes {
				if err := r.Options.validate(r.Host, r.TLSMode); err != nil {
					t.Fatalf("fixture %s invalid: %v", r.Host, err)
				}
			}
			cfg, blocked := buildConfig(routes, tt.settings)
			if len(blocked) > 0 {
				t.Fatalf("blocked = %v", blocked)
			}
			checkGolden(t, filepath.Join("testdata", "config", tt.name+".json"), cfg)
		})
	}
}

func checkGolden(t *testing.T, path string, cfg *caddyConfig) {
	t.Helper()
	got, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
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
}

package proxy

import (
	"encoding/json"
	"testing"
)

func TestBuildConfigSplitsPlainAndTLS(t *testing.T) {
	routes := []Route{
		{Host: "plain.lan", Upstream: "127.0.0.1:8080", TLSMode: "none", Enabled: true},
		{Host: "app.example.com", Upstream: "127.0.0.1:3000", TLSMode: "auto", Enabled: true},
		{Host: "int.lan", Upstream: "127.0.0.1:3001", TLSMode: "internal", Enabled: true},
		{Host: "off.lan", Upstream: "127.0.0.1:3002", TLSMode: "auto", Enabled: false},
		{Host: "evil.lan", Upstream: "localhost:2019", TLSMode: "none", Enabled: true},
	}
	cfg, blocked := buildConfig(routes)

	if len(blocked) != 1 || blocked[0] != "evil.lan" {
		t.Fatalf("blocked = %v", blocked)
	}
	plain := cfg.Apps.HTTP.Servers[serverHTTP]
	if len(plain.Listen) != 1 || plain.Listen[0] != ":80" {
		t.Fatalf("plain listen = %v", plain.Listen)
	}
	if hosts := routeHosts(plain); len(hosts) != 1 || hosts[0] != "plain.lan" {
		t.Fatalf("plain routes = %v", hosts)
	}
	secure := cfg.Apps.HTTP.Servers[serverHTTPS]
	if len(secure.Listen) != 1 || secure.Listen[0] != ":443" {
		t.Fatalf("tls listen = %v", secure.Listen)
	}
	if hosts := routeHosts(secure); len(hosts) != 2 || hosts[0] != "app.example.com" || hosts[1] != "int.lan" {
		t.Fatalf("tls routes = %v", hosts)
	}
	if cfg.Apps.TLS == nil || len(cfg.Apps.TLS.Automation.Policies) != 2 {
		t.Fatalf("tls policies = %+v", cfg.Apps.TLS)
	}
	if !cfg.Admin.EnforceOrigin || cfg.Admin.Listen != adminAddr {
		t.Fatalf("admin = %+v", cfg.Admin)
	}
}

func TestBuildConfigWithoutTLSRoutesHasNoHTTPSServer(t *testing.T) {
	cfg, _ := buildConfig([]Route{{Host: "a.lan", Upstream: "10.0.0.2:80", TLSMode: "none", Enabled: true}})
	if _, ok := cfg.Apps.HTTP.Servers[serverHTTPS]; ok {
		t.Fatal("HTTPS server present without TLS routes")
	}
	if cfg.Apps.TLS != nil {
		t.Fatal("TLS app present without TLS routes")
	}
}

func routeHosts(s caddyServer) []string {
	var out []string
	for _, r := range s.Routes {
		out = append(out, r.Match[0].Host...)
	}
	return out
}

func TestCheckUpstream(t *testing.T) {
	tests := []struct {
		in   string
		want error
	}{
		{"127.0.0.1:8080", nil},
		{"app.lan:3000", nil},
		{"my_service:80", nil},
		{"[::1]:8080", nil},
		{"192.168.1.10:443", nil},
		{"localhost:2019", ErrAdminUpstream},
		{"127.0.0.1:2019", ErrAdminUpstream},
		{"127.0.0.1:02019", ErrAdminUpstream},
		{"0.0.0.0:2019", ErrAdminUpstream},
		{"[::1]:2019", ErrAdminUpstream},
		{"LOCALHOST.:2019", ErrInvalidUpstream},
		{"10.0.0.5:2019", nil},
		{"http://app:80", ErrInvalidUpstream},
		{"app", ErrInvalidUpstream},
		{"app:0", ErrInvalidUpstream},
		{"app:70000", ErrInvalidUpstream},
		{":8080", ErrInvalidUpstream},
		{"{http.request.host}:80", ErrInvalidUpstream},
		{"unix//run/app.sock", ErrInvalidUpstream},
		{"app/path:80", ErrInvalidUpstream},
	}
	for _, tt := range tests {
		if got := checkUpstream(tt.in); got != tt.want {
			t.Errorf("checkUpstream(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSameConfig(t *testing.T) {
	cfg, _ := buildConfig([]Route{{Host: "a.lan", Upstream: "10.0.0.2:80", TLSMode: "auto", Enabled: true}})
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !sameConfig(b, cfg) {
		t.Fatal("identical config reported as different")
	}
	if sameConfig([]byte(`{"admin":{"listen":"127.0.0.1:2019"}}`), cfg) {
		t.Fatal("bootstrap config reported as current")
	}
	if sameConfig([]byte(`null`), cfg) {
		t.Fatal("empty config reported as current")
	}
}

func TestSpecHashFollowsImage(t *testing.T) {
	a := NewService(nil, nil, false)
	b := NewService(nil, nil, false)
	if a.specHash() != b.specHash() {
		t.Fatal("spec hash not stable")
	}
	b.SetImage("caddy:2.99.0")
	if a.specHash() == b.specHash() {
		t.Fatal("spec hash ignores the image")
	}
}

func TestTargetsAdminCatchesStoredForms(t *testing.T) {
	for _, dial := range []string{"LOCALHOST.:2019", " localhost:2019", ":2019", "127.0.0.2:2019"} {
		if !targetsAdmin(dial) {
			t.Errorf("targetsAdmin(%q) = false", dial)
		}
	}
}

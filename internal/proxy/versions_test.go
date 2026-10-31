package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name string
		g    Settings
		ok   bool
	}{
		{"defaults", Settings{}, true},
		{"email", Settings{ACMEEmail: "ops@example.com"}, true},
		{"staging", Settings{ACMECA: "letsencrypt_staging"}, true},
		{"zerossl with email", Settings{ACMECA: "zerossl", ACMEEmail: "ops@example.com"}, true},
		{"zerossl without email", Settings{ACMECA: "zerossl"}, false},
		{"unknown ca", Settings{ACMECA: "https://ca.example/dir"}, false},
		{"display name", Settings{ACMEEmail: "Ops <ops@example.com>"}, false},
		{"placeholder", Settings{ACMEEmail: "{env.X}@example.com"}, false},
		{"cloudflare", Settings{TrustedProxies: "cloudflare"}, true},
		{"unknown preset", Settings{TrustedProxies: "fastly"}, false},
	}
	for _, tt := range tests {
		if err := tt.g.validate(); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
}

func TestBuildConfigAppliesSettings(t *testing.T) {
	routes := []Route{
		site("auto", SiteOptions{}),
		{Host: "int.lan", Upstream: "10.0.0.6:80", TLSMode: "internal", Enabled: true},
	}
	cfg, _ := buildConfig(routes, Settings{ACMEEmail: "ops@example.com", ACMECA: "zerossl", TrustedProxies: "cloudflare"})
	pol := cfg.Apps.TLS.Automation.Policies
	if pol[0].Issuers[0] != (caddyIssuer{Module: "acme", CA: "https://acme.zerossl.com/v2/DV90", Email: "ops@example.com"}) {
		t.Errorf("acme issuer = %+v", pol[0].Issuers[0])
	}
	if pol[1].Issuers[0] != (caddyIssuer{Module: "internal"}) {
		t.Errorf("internal issuer = %+v", pol[1].Issuers[0])
	}
	for name, srv := range cfg.Apps.HTTP.Servers {
		if srv.TrustedProxies == nil || len(srv.TrustedProxies.Ranges) != len(cloudflareRanges) {
			t.Errorf("%s: trusted proxies = %+v", name, srv.TrustedProxies)
		}
	}
	b, _ := json.Marshal(cfg)
	if !strings.Contains(string(b), `"client_ip_headers":["CF-Connecting-IP"]`) {
		t.Errorf("client IP header not set: %s", b)
	}
	if !strings.Contains(string(b), `"trusted_proxies":{"source":"static","ranges":["173.245.48.0/20"`) {
		t.Errorf("trusted proxies JSON: %s", b)
	}
	for _, r := range cloudflareRanges {
		if _, err := parseIPRange(r); err != nil {
			t.Errorf("bad Cloudflare range %s", r)
		}
	}
}

func TestTestedVersions(t *testing.T) {
	if TestedVersions[len(TestedVersions)-1].Image != DefaultProxyImage {
		t.Fatal("the default image is not the newest tested version")
	}
	for _, v := range TestedVersions {
		if !strings.Contains(v.Image, ":"+v.Version+"@sha256:") {
			t.Errorf("%s is not pinned by tag and digest: %s", v.Version, v.Image)
		}
	}
	if got := digestRef("caddy:2.11.6@sha256:abc"); got != "caddy@sha256:abc" {
		t.Errorf("digestRef = %s", got)
	}
	if got := digestRef("registry:5000/caddy:2@sha256:abc"); got != "registry:5000/caddy@sha256:abc" {
		t.Errorf("digestRef with registry port = %s", got)
	}
}

// versionService is a service whose container start is faked: failImage
// makes starting that image fail.
func versionService(t *testing.T, failImage string) (*Service, memSettings, *[]string) {
	t.Helper()
	s := newServiceDB(t)
	st := memSettings{}
	s.SetSettings(st)
	var started []string
	s.startProxy = func(context.Context) error {
		img := s.currentImage()
		started = append(started, img)
		if img == failImage {
			return errors.New("caddy exited")
		}
		return nil
	}
	return s, st, &started
}

func TestSwitchVersion(t *testing.T) {
	ctx := context.Background()
	older, newer := TestedVersions[0], TestedVersions[len(TestedVersions)-1]

	s, st, started := versionService(t, "")
	s.enabled.Store(true)
	info, err := s.SwitchVersion(ctx, older.Version)
	if err != nil {
		t.Fatal(err)
	}
	if info.Current != older.Version || info.Previous != newer.Version || st[KeyCaddyVersion] != older.Version {
		t.Fatalf("after switch: %+v, settings %v", info, st)
	}
	if len(*started) != 1 || (*started)[0] != older.Image {
		t.Fatalf("started = %v", *started)
	}
	info, err = s.RollbackVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Current != newer.Version || info.Previous != older.Version {
		t.Fatalf("after rollback: %+v", info)
	}
	if _, err := s.SwitchVersion(ctx, "9.9.9"); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("unknown version: err = %v", err)
	}
}

func TestSwitchVersionRollsBackWhenCaddyFails(t *testing.T) {
	ctx := context.Background()
	older, newer := TestedVersions[0], TestedVersions[len(TestedVersions)-1]
	s, st, started := versionService(t, older.Image)
	s.enabled.Store(true)
	_, err := s.SwitchVersion(ctx, older.Version)
	if !errors.Is(err, ErrVersionSwitchFailed) || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	if s.currentImage() != newer.Image || st[KeyCaddyVersion] != "" || st[KeyCaddyPrevious] != "" {
		t.Fatalf("not rolled back: image %s, settings %v", s.currentImage(), st)
	}
	if len(*started) != 2 || (*started)[1] != newer.Image {
		t.Fatalf("started = %v", *started)
	}
}

func TestSwitchVersionWhileProxyOff(t *testing.T) {
	ctx := context.Background()
	older := TestedVersions[0]
	s, st, started := versionService(t, "")
	if _, err := s.SwitchVersion(ctx, older.Version); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 || st[KeyCaddyVersion] != older.Version || s.currentImage() != older.Image {
		t.Fatalf("started %v, settings %v", *started, st)
	}
}

func TestStoredVersionIsApplied(t *testing.T) {
	older := TestedVersions[0]
	s := NewService(nil, nil, false)
	s.SetSettings(memSettings{KeyCaddyVersion: older.Version})
	if s.currentImage() != older.Image {
		t.Fatalf("image = %s", s.currentImage())
	}
	stale := NewService(nil, nil, false)
	stale.SetSettings(memSettings{KeyCaddyVersion: "2.0.0"})
	if stale.currentImage() != DefaultProxyImage {
		t.Fatalf("stale version image = %s", stale.currentImage())
	}
}

func TestVersionReadOnlyWithImageOverride(t *testing.T) {
	s := NewService(nil, nil, false)
	s.SetImage("registry.example/caddy:custom")
	s.SetSettings(memSettings{KeyCaddyVersion: TestedVersions[0].Version, KeyCaddyPrevious: TestedVersions[0].Version})
	if s.currentImage() != "registry.example/caddy:custom" {
		t.Fatalf("stored version replaced the override: %s", s.currentImage())
	}
	info := s.VersionInfo()
	if info.Managed || info.ReadOnlyReason == "" || info.Current != "" {
		t.Fatalf("info = %+v", info)
	}
	if _, err := s.SwitchVersion(context.Background(), TestedVersions[0].Version); !errors.Is(err, ErrVersionReadOnly) {
		t.Fatalf("switch: err = %v", err)
	}
	if _, err := s.RollbackVersion(context.Background()); !errors.Is(err, ErrVersionReadOnly) {
		t.Fatalf("rollback: err = %v", err)
	}
}

func TestUpdateRouteKeepsEnabledWhenOmitted(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRouteEnabled(ctx, r.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoute(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Upstream != "10.0.0.2:80" {
		t.Fatalf("route = %+v", got)
	}
	on := true
	if err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", &on, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRoute(ctx, r.ID); !got.Enabled {
		t.Fatal("explicit enabled=true ignored")
	}
}

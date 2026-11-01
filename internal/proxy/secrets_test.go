package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSecretHeaderWriteOnly(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	opts := SiteOptions{Headers: HeaderOptions{
		RequestSet:  []Header{{Name: "Authorization", Value: "Bearer tok-1", Secret: true}, {Name: "X-Plain", Value: "visible"}},
		ResponseSet: []Header{{Name: "X-Robots-Tag", Value: "noindex"}},
	}}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	pub := r.Options.Headers.RequestSet
	if pub[0].Value != "" || !pub[0].ValueSet || !pub[0].Secret || pub[1].Value != "visible" || pub[1].ValueSet {
		t.Fatalf("public headers: %+v", pub)
	}

	stored := func() SiteOptions {
		t.Helper()
		got, err := s.getRoute(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Options
	}

	keep := func() []Header { return []Header{{Name: "authorization", Secret: true}} }
	tests := []struct {
		name     string
		upstream string
		extra    []string
		req, rsp []Header
		wantReq  string
		wantErr  string
	}{
		{"empty keeps, any case", "10.0.0.2:80", nil, keep(), nil, "Bearer tok-1", ""},
		{"public response sent back keeps", "10.0.0.2:80", nil, pub[:1], nil, "Bearer tok-1", ""},
		{"new value replaces", "10.0.0.2:80", nil, []Header{{Name: "Authorization", Value: "Bearer tok-2", Secret: true}}, nil, "Bearer tok-2", ""},
		{"no stored value", "10.0.0.2:80", nil, []Header{{Name: "X-Api-Key", Secret: true}}, nil, "", "headers.request_set"},
		{"response headers can't be secret", "10.0.0.2:80", nil, nil, []Header{{Name: "X-Key", Value: "v", Secret: true}}, "", "headers.response_set"},
		{"other upstream doesn't keep", "10.0.0.9:80", nil, keep(), nil, "", "headers.request_set"},
		{"added upstream doesn't keep", "10.0.0.2:80", []string{"10.0.0.9:80"}, keep(), nil, "", "headers.request_set"},
		{"other upstream with the value again", "10.0.0.9:80", nil, []Header{{Name: "Authorization", Value: "Bearer tok-3", Secret: true}}, nil, "Bearer tok-3", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := SiteOptions{Routing: RoutingOptions{ExtraUpstreams: tt.extra}, Headers: HeaderOptions{RequestSet: tt.req, ResponseSet: tt.rsp}}
			_, err := s.UpdateRoute(ctx, r.ID, tt.upstream, "auto", nil, &next)
			if tt.wantErr != "" {
				var oe *OptionError
				if !errors.As(err, &oe) || oe.Field != tt.wantErr {
					t.Fatalf("err = %v, want field %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := stored()
			if tt.wantReq != "" && got.Headers.RequestSet[0].Value != tt.wantReq {
				t.Errorf("request value = %q, want %q", got.Headers.RequestSet[0].Value, tt.wantReq)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "value_set") {
				t.Errorf("value_set stored: %s", raw)
			}
			reset := opts
			if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, &reset); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestForwardAuthQueryWriteOnly(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	const full = "http://authelia:9091/api/verify?token=fa-s3cret"
	opts := SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: full}}}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	if fa := r.Options.Access.ForwardAuth; fa.URL != "http://authelia:9091/api/verify" || !fa.QuerySet {
		t.Fatalf("public forward auth: %+v", fa)
	}

	tests := []struct {
		name    string
		in      ForwardAuth
		want    string
		wantErr bool
	}{
		{"no query keeps", ForwardAuth{URL: "http://authelia:9091/api/verify"}, full, false},
		{"other path same server keeps", ForwardAuth{URL: "http://AUTHELIA:9091/api/authz"}, "http://AUTHELIA:9091/api/authz?token=fa-s3cret", false},
		{"new query replaces", ForwardAuth{URL: "http://authelia:9091/api/verify?token=new"}, "http://authelia:9091/api/verify?token=new", false},
		{"clear drops", ForwardAuth{URL: "http://authelia:9091/api/verify", ClearQuery: true}, "http://authelia:9091/api/verify", false},
		{"other server refuses", ForwardAuth{URL: "http://evil.example.net/verify"}, "", true},
		{"other scheme refuses", ForwardAuth{URL: "https://authelia:9091/api/verify"}, "", true},
		{"other server with clear", ForwardAuth{URL: "http://other:9091/verify", ClearQuery: true}, "http://other:9091/verify", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			next := SiteOptions{Access: AccessOptions{ForwardAuth: &in}}
			_, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, &next)
			if tt.wantErr {
				var oe *OptionError
				if !errors.As(err, &oe) || oe.Field != "access.forward_auth.url" {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s.getRoute(ctx, r.ID)
			fa := got.Options.Access.ForwardAuth
			if fa.URL != tt.want || fa.ClearQuery || fa.QuerySet {
				t.Errorf("stored %+v, want url %s", fa, tt.want)
			}
			reset := SiteOptions{Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: full}}}
			if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.2:80", "auto", nil, &reset); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreviewHidesSecretsFromEditors(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	opts := SiteOptions{
		Access: AccessOptions{ForwardAuth: &ForwardAuth{URL: "http://authelia:9091/api/verify?token=fa-s3cret"}},
		Headers: HeaderOptions{RequestSet: []Header{
			{Name: "Authorization", Value: "Bearer hdr-s3cret", Secret: true},
			{Name: "X-Plain", Value: "hdr-plain"},
		}},
	}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []bool{false, true} {
		p, err := s.SiteConfig(ctx, r.ID, mask)
		if err != nil {
			t.Fatal(err)
		}
		if p.Blocked {
			t.Fatalf("mask=%v: preview blocked", mask)
		}
		raw, _ := json.Marshal(p.Config)
		body := string(raw)
		for _, secret := range []string{"hdr-s3cret", "fa-s3cret"} {
			if strings.Contains(body, secret) {
				t.Errorf("mask=%v: %s in preview", mask, secret)
			}
		}
		if got := strings.Contains(body, "hdr-plain"); got == mask {
			t.Errorf("mask=%v: plain header shown = %v", mask, got)
		}
		if !strings.Contains(body, "Authorization") || !strings.Contains(body, "authelia:9091") {
			t.Errorf("mask=%v: header name or auth host missing", mask)
		}
	}
}

func TestUpdateRouteReportsChanges(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	opts := SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "u1", Password: "password-1"}}}}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.2:80", TLSMode: "auto", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	on := false
	tests := []struct {
		name     string
		upstream string
		tls      string
		enabled  *bool
		opts     *SiteOptions
		want     []string
	}{
		{"nothing", "10.0.0.2:80", "auto", nil, nil, []string{}},
		{"public options sent back", "10.0.0.2:80", "auto", nil, &SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "u1"}}}, Headers: HeaderOptions{RequestSet: []Header{}}}, []string{}},
		{"new password", "10.0.0.2:80", "auto", nil, &SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "u1", Password: "password-2"}}}}, []string{"options.access"}},
		{"destination and groups", "10.0.0.3:80", "internal", &on, &SiteOptions{Access: AccessOptions{BasicAuth: []BasicAuthUser{{Username: "u1"}}}, Performance: PerformanceOptions{ReadTimeout: "1h"}, Logs: LogOptions{AccessLog: true}}, []string{"upstream", "tls_mode", "enabled", "options.performance", "options.logs"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.UpdateRoute(ctx, r.ID, tt.upstream, tt.tls, tt.enabled, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("changed = %v, want %v", got, tt.want)
			}
		})
	}
}

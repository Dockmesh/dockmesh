package setup

import (
	"strconv"
	"testing"
)

func TestApplyValuesProxy(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*ApplyValues)
		ok    bool
		proxy bool
		route string
	}{
		{name: "off", edit: func(*ApplyValues) {}, ok: true},
		{name: "on without email", edit: func(v *ApplyValues) { v.Proxy.Enabled = true }, ok: true, proxy: true},
		{name: "on with email", edit: func(v *ApplyValues) { v.Proxy.Enabled, v.Proxy.ACMEEmail = true, "ops@example.org" }, ok: true, proxy: true},
		{name: "email without proxy", edit: func(v *ApplyValues) { v.Proxy.ACMEEmail = "ops@example.org" }},
		{name: "bad email", edit: func(v *ApplyValues) { v.Proxy.Enabled, v.Proxy.ACMEEmail = true, "ops@example.org\nx" }},
		{name: "caddy turns the proxy on", edit: func(v *ApplyValues) {
			v.TLSMode, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "https://dm.example.org", "ops@example.org"
		}, ok: true, proxy: true, route: "dm.example.org:8080"},
		{name: "caddy on another port", edit: func(v *ApplyValues) {
			v.TLSMode, v.HTTPAddr, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "127.0.0.1:9000", "https://dm.example.org", "ops@example.org"
		}, ok: true, proxy: true, route: "dm.example.org:9000"},
		{name: "caddy on every address", edit: func(v *ApplyValues) {
			v.TLSMode, v.HTTPAddr, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "0.0.0.0:8080", "https://dm.example.org", "ops@example.org"
		}},
		{name: "caddy on its own port", edit: func(v *ApplyValues) {
			v.TLSMode, v.HTTPAddr, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "127.0.0.1:443", "https://dm.example.org", "ops@example.org"
		}},
		{name: "caddy for localhost", edit: func(v *ApplyValues) {
			v.TLSMode, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "https://dm.localhost", "ops@example.org"
		}},
		{name: "caddy without email", edit: func(v *ApplyValues) { v.TLSMode, v.BaseURL = "caddy", "https://dm.example.org" }},
		{name: "caddy with an IP", edit: func(v *ApplyValues) {
			v.TLSMode, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "https://192.0.2.10", "ops@example.org"
		}},
		{name: "caddy over http", edit: func(v *ApplyValues) {
			v.TLSMode, v.BaseURL, v.Proxy.ACMEEmail = "caddy", "http://dm.example.org", "ops@example.org"
		}},
		{name: "restore with proxy", edit: func(v *ApplyValues) {
			v.Restore, v.Admin.Username, v.Admin.PasswordHash, v.Proxy.Enabled = true, "", "", true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validValues()
			tt.edit(&v)
			err := v.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate: %v, want ok=%v", err, tt.ok)
			}
			if err != nil {
				return
			}
			if v.Proxy.Enabled != tt.proxy {
				t.Errorf("proxy enabled %v, want %v", v.Proxy.Enabled, tt.proxy)
			}
			host, port, ok := v.CaddyRoute()
			got := ""
			if ok {
				got = host + ":" + strconv.Itoa(port)
			}
			if got != tt.route {
				t.Errorf("caddy route %q, want %q", got, tt.route)
			}
		})
	}
}

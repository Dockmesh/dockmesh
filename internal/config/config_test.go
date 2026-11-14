package config

import (
	"testing"
	"time"
)

func TestParseEnrollTokenTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, true},
		{"2h", 2 * time.Hour, true},
		{"720h", 720 * time.Hour, true},
		{"30s", 0, false},
		{"721h", 0, false},
		{"-1h", 0, false},
		{"tomorrow", 0, false},
	}
	for _, c := range cases {
		got, err := parseEnrollTokenTTL(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parseEnrollTokenTTL(%q) = %v, %v; want %v ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestLoadTLSMode(t *testing.T) {
	cases := []struct {
		mode, addr string
		wantAddr   string
		ok         bool
	}{
		{"", "", ":8080", true},
		{"internal", "", ":8080", true},
		{"proxy", "", "127.0.0.1:8080", true},
		{"caddy", "", "127.0.0.1:8080", true},
		{"proxy", "10.0.0.5:9000", "10.0.0.5:9000", true},
		{"tls", "", "", false},
		{"file", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.mode+"/"+c.addr, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DOCKMESH_DATA_DIR", dir)
			t.Setenv("DOCKMESH_TLS_MODE", c.mode)
			t.Setenv("DOCKMESH_HTTP_ADDR", c.addr)
			cfg, err := Load()
			if (err == nil) != c.ok {
				t.Fatalf("Load: %v, want ok=%v", err, c.ok)
			}
			if err == nil && cfg.HTTPAddr != c.wantAddr {
				t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, c.wantAddr)
			}
		})
	}
}

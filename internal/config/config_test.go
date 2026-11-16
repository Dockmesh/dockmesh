package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// A symlink planted at the secrets path is replaced, never written through.
func TestJWTSecretReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		setup func(path string)
	}{
		{"symlink to a file", func(p string) {
			if err := os.Symlink(victim, p); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(p string) {
			if err := os.Symlink(filepath.Join(dir, "missing"), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"file without the key", func(p string) {
			if err := os.WriteFile(p, []byte("OTHER=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("secrets-%d.env", i))
			tt.setup(path)
			secret, err := loadOrCreateJWTSecret(path)
			if err != nil || len(secret) != 32 {
				t.Fatalf("secret %d bytes, err %v", len(secret), err)
			}
			if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Fatalf("secrets file: %v, %v", fi, err)
			}
			if again, err := loadOrCreateJWTSecret(path); err != nil || string(again) != string(secret) {
				t.Fatalf("second load = %x, %v", again, err)
			}
			if data, _ := os.ReadFile(victim); string(data) != "keep\n" {
				t.Fatalf("symlink target written: %q", data)
			}
			if _, err := os.Lstat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
				t.Fatalf("dangling target created: %v", err)
			}
		})
	}
}

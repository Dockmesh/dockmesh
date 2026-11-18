package setup

import (
	"strings"
	"testing"
)

func validValues() ApplyValues {
	var v ApplyValues
	v.DataDir = "/var/lib/dockmesh"
	v.BaseURL = "https://dm.lan:8080"
	v.Admin.Username = "admin"
	v.Admin.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	return v
}

func TestApplyValuesValidate(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*ApplyValues)
		ok     bool
		addr   string
		mode   string
		trusts string
	}{
		{name: "defaults", edit: func(*ApplyValues) {}, ok: true, addr: ":8080", mode: "internal"},
		{name: "proxy on loopback", edit: func(v *ApplyValues) { v.TLSMode = "proxy" }, ok: true, addr: "127.0.0.1:8080", mode: "proxy", trusts: "127.0.0.1"},
		{name: "proxy on LAN address", edit: func(v *ApplyValues) { v.TLSMode = "proxy"; v.HTTPAddr = "10.0.0.5:8080" }, ok: true, addr: "10.0.0.5:8080", mode: "proxy", trusts: "10.0.0.5"},
		{name: "proxy on every address", edit: func(v *ApplyValues) { v.TLSMode = "proxy"; v.HTTPAddr = ":8080" }, ok: true, addr: ":8080", mode: "proxy"},
		{name: "caddy", edit: func(v *ApplyValues) { v.TLSMode, v.Proxy.ACMEEmail = "caddy", "ops@dm.lan" }, ok: true, addr: "127.0.0.1:8080", mode: "caddy", trusts: "127.0.0.1"},
		{name: "own certificate", edit: func(v *ApplyValues) {
			v.TLSMode, v.TLSCertFile, v.TLSKeyFile = "file", "/etc/ssl/dm.pem", "/etc/ssl/dm.key"
		}, ok: true, addr: ":8080", mode: "file"},
		{name: "own certificate, relative", edit: func(v *ApplyValues) { v.TLSMode, v.TLSCertFile, v.TLSKeyFile = "file", "dm.pem", "/k" }},
		{name: "cert files without mode file", edit: func(v *ApplyValues) { v.TLSCertFile = "/etc/ssl/dm.pem" }},
		{name: "unknown mode", edit: func(v *ApplyValues) { v.TLSMode = "none" }},
		{name: "newline in data dir", edit: func(v *ApplyValues) { v.DataDir = "/srv/dm\nDOCKMESH_X=1" }},
		{name: "relative data dir", edit: func(v *ApplyValues) { v.DataDir = "srv/dm" }},
		{name: "system data dir", edit: func(v *ApplyValues) { v.DataDir = "/etc/dockmesh" }},
		{name: "inside docker", edit: func(v *ApplyValues) { v.DataDir = "/var/lib/docker/volumes/x" }},
		{name: "plain /var", edit: func(v *ApplyValues) { v.DataDir = "/var" }},
		{name: "/data", edit: func(v *ApplyValues) { v.DataDir = "/data" }, ok: true, addr: ":8080", mode: "internal"},
		{name: "host name listen address", edit: func(v *ApplyValues) { v.HTTPAddr = "dm.lan:8080" }},
		{name: "newline in listen address", edit: func(v *ApplyValues) { v.HTTPAddr = ":8080\nX=1" }},
		{name: "bad base URL", edit: func(v *ApplyValues) { v.BaseURL = "javascript:alert(1)" }},
		{name: "bad admin name", edit: func(v *ApplyValues) { v.Admin.Username = "Admin User" }},
		{name: "plaintext password", edit: func(v *ApplyValues) { v.Admin.PasswordHash = "secret123456" }},
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
			if v.HTTPAddr != tt.addr || v.TLSMode != tt.mode {
				t.Errorf("addr %q mode %q", v.HTTPAddr, v.TLSMode)
			}
			env, err := v.ConfigEnv("/usr/local/share/dockmesh")
			if err != nil {
				t.Fatal(err)
			}
			got := string(env)
			for _, want := range []string{"DOCKMESH_DATA_DIR=" + v.DataDir + "\n", "DOCKMESH_TLS_MODE=" + tt.mode + "\n", "DOCKMESH_HTTP_ADDR=" + tt.addr + "\n"} {
				if !strings.Contains(got, want) {
					t.Errorf("config lacks %q:\n%s", want, got)
				}
			}
			if hasTrust := strings.Contains(got, "DOCKMESH_TRUSTED_PROXIES="+tt.trusts+"\n"); tt.trusts != "" && !hasTrust {
				t.Errorf("config lacks trusted proxies %s:\n%s", tt.trusts, got)
			}
			if tt.trusts == "" && strings.Contains(got, "DOCKMESH_TRUSTED_PROXIES") {
				t.Errorf("config trusts a proxy:\n%s", got)
			}
			if strings.Contains(got, "SETUP_FORCE") || strings.Contains(got, "argon2") {
				t.Errorf("config carries setup state:\n%s", got)
			}
		})
	}
}

func TestApplyValuesRestore(t *testing.T) {
	tests := []struct {
		name string
		v    ApplyValues
		ok   bool
	}{
		{name: "restore without admin", v: ApplyValues{DataDir: "/srv/dockmesh", Restore: true}, ok: true},
		{name: "restore with admin", v: func() ApplyValues {
			v := ApplyValues{DataDir: "/srv/dockmesh", Restore: true}
			v.Admin.Username = "admin"
			return v
		}(), ok: false},
		{name: "restore into system dir", v: ApplyValues{DataDir: "/etc/dockmesh", Restore: true}, ok: false},
		{name: "new install needs an admin", v: ApplyValues{DataDir: "/srv/dockmesh"}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.v.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

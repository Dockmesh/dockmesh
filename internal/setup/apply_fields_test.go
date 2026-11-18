package setup

import (
	"strings"
	"testing"
)

func TestApplyValuesNetworkFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ApplyValues)
		ok   bool
		// want are lines the config must hold, deny substrings it must not.
		want []string
		deny []string
	}{
		{name: "defaults", edit: func(*ApplyValues) {}, ok: true,
			want: []string{"DOCKMESH_AGENT_LISTEN=:8443\n"}, deny: []string{"DOCKMESH_STACKS_ROOT", "DOCKMESH_TRUSTED_PROXIES"}},
		{name: "default stacks dir spelled out", edit: func(v *ApplyValues) { v.StacksDir = "/var/lib/dockmesh/stacks" }, ok: true,
			deny: []string{"DOCKMESH_STACKS_ROOT"}},
		{name: "stacks dir outside", edit: func(v *ApplyValues) { v.StacksDir = "/srv/stacks" }, ok: true,
			want: []string{"DOCKMESH_STACKS_ROOT=/srv/stacks\n"}},
		{name: "stacks dir inside the data dir", edit: func(v *ApplyValues) { v.StacksDir = "/var/lib/dockmesh/other" }},
		{name: "stacks dir holds the data dir", edit: func(v *ApplyValues) { v.StacksDir = "/var/lib" }},
		{name: "stacks dir in a system tree", edit: func(v *ApplyValues) { v.StacksDir = "/etc/stacks" }},
		{name: "stacks dir relative", edit: func(v *ApplyValues) { v.StacksDir = "srv/stacks" }},
		{name: "stacks dir unclean", edit: func(v *ApplyValues) { v.StacksDir = "/srv/../etc" }},
		{name: "stacks dir with a newline", edit: func(v *ApplyValues) { v.StacksDir = "/srv/s\nX=1" }},
		{name: "agent port", edit: func(v *ApplyValues) { v.AgentListen = ":9443" }, ok: true,
			want: []string{"DOCKMESH_AGENT_LISTEN=:9443\n"}},
		{name: "agent port with a host", edit: func(v *ApplyValues) { v.AgentListen = "0.0.0.0:9443" }},
		{name: "agent port zero", edit: func(v *ApplyValues) { v.AgentListen = ":0" }},
		{name: "agent port too high", edit: func(v *ApplyValues) { v.AgentListen = ":70000" }},
		{name: "agent port with a leading zero", edit: func(v *ApplyValues) { v.AgentListen = ":08443" }},
		{name: "agent port equals web port", edit: func(v *ApplyValues) { v.AgentListen = ":8080" }},
		{name: "agent URL", edit: func(v *ApplyValues) { v.AgentURL = "wss://agents.example.org:443/connect" }, ok: true,
			deny: []string{"agents.example.org"}},
		{name: "agent URL over https", edit: func(v *ApplyValues) { v.AgentURL = "https://agents.example.org/connect" }},
		{name: "agent URL with a space", edit: func(v *ApplyValues) { v.AgentURL = "wss://a b/connect" }},
		{name: "trusted proxies", edit: func(v *ApplyValues) {
			v.TLSMode, v.TrustedProxies = "proxy", "127.0.0.1, 10.0.0.0/24;fd00::1"
		}, ok: true, want: []string{"DOCKMESH_TRUSTED_PROXIES=127.0.0.1/32,10.0.0.0/24,fd00::1/128\n"}},
		{name: "trusted proxies trust everyone", edit: func(v *ApplyValues) { v.TLSMode, v.TrustedProxies = "proxy", "0.0.0.0/0" }},
		{name: "trusted proxies trust every IPv6 address", edit: func(v *ApplyValues) { v.TLSMode, v.TrustedProxies = "proxy", "10.0.0.1,::/0" }},
		{name: "trusted proxies not an address", edit: func(v *ApplyValues) { v.TLSMode, v.TrustedProxies = "proxy", "proxy.lan" }},
		{name: "trusted proxies without proxy mode", edit: func(v *ApplyValues) { v.TrustedProxies = "10.0.0.1" }},
		{name: "trusted proxies with caddy", edit: func(v *ApplyValues) { v.TLSMode, v.TrustedProxies = "caddy", "10.0.0.1" }},
		{name: "too many trusted proxies", edit: func(v *ApplyValues) {
			v.TLSMode, v.TrustedProxies = "proxy", strings.Repeat("10.0.0.1,", 33)+"10.0.0.2"
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
			env, err := v.ConfigEnv("/usr/local/share/dockmesh")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(env), w) {
					t.Errorf("config lacks %q:\n%s", w, env)
				}
			}
			for _, d := range tt.deny {
				if strings.Contains(string(env), d) {
					t.Errorf("config holds %q:\n%s", d, env)
				}
			}
		})
	}
}

func TestStacksRoot(t *testing.T) {
	v := validValues()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := v.StacksRoot(); got != "/var/lib/dockmesh/stacks" || v.StacksOutsideDataDir() {
		t.Errorf("default: %s outside=%v", got, v.StacksOutsideDataDir())
	}
	v.StacksDir = "/srv/stacks"
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := v.StacksRoot(); got != "/srv/stacks" || !v.StacksOutsideDataDir() {
		t.Errorf("outside: %s outside=%v", got, v.StacksOutsideDataDir())
	}
}

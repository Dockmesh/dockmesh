package main

import (
	"os"
	"testing"
)

func TestResolveDataDir(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		present []string
		want    string
	}{
		{"fresh host", "", nil, "/var/lib/dockmesh-agent"},
		{"installer layout", "", []string{"/var/lib/dockmesh-agent/agent.crt"}, "/var/lib/dockmesh-agent"},
		{"enrolled under the old default", "", []string{"/var/lib/dockmesh/agent.crt"}, "/var/lib/dockmesh"},
		{"both enrolled: new default wins", "", []string{"/var/lib/dockmesh/agent.crt", "/var/lib/dockmesh-agent/agent.crt"}, "/var/lib/dockmesh-agent"},
		{"env set to the old default", "/var/lib/dockmesh", nil, "/var/lib/dockmesh"},
		{"env wins over files", "/srv/agent", []string{"/var/lib/dockmesh/agent.crt"}, "/srv/agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := func(p string) bool {
				for _, q := range tt.present {
					if p == q {
						return true
					}
				}
				return false
			}
			if got := resolveDataDir(tt.env, exists); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("DOCKMESH_AGENT_URL", "wss://from-shell/connect")
	for _, k := range []string{"DOCKMESH_DATA_DIR", "DOCKMESH_TOKEN", "PATH_INJECT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	applyEnv([]byte("# agent\nDOCKMESH_DATA_DIR=/var/lib/dockmesh-agent\nDOCKMESH_AGENT_URL=wss://file/connect\nDOCKMESH_TOKEN=\"t\"\nPATH_INJECT=x\n"))
	tests := []struct{ key, want string }{
		{"DOCKMESH_DATA_DIR", "/var/lib/dockmesh-agent"},
		{"DOCKMESH_AGENT_URL", "wss://from-shell/connect"},
		{"DOCKMESH_TOKEN", "t"},
		{"PATH_INJECT", ""},
	}
	for _, tt := range tests {
		if got := os.Getenv(tt.key); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.key, got, tt.want)
		}
	}
}

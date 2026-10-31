package main

import (
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/host"
)

func TestStagedMountBody(t *testing.T) {
	tests := []struct {
		name         string
		mount        host.StagedMount
		wantUpgrade  bool
		wantRedeploy string
	}{
		{name: "local host", mount: host.StagedMount{HostID: "local", HostName: "Local", Stack: "web"}, wantRedeploy: "Redeploy it:"},
		{name: "agent host", mount: host.StagedMount{HostID: "3f2a", HostName: "edge-1", Stack: "web"}, wantUpgrade: true, wantRedeploy: "redeploy the stack afterwards:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stagedMountBody(tt.mount)
			if has := strings.Contains(got, "Upgrade the agent first (Hosts → edge-1 → Upgrade agent)"); has != tt.wantUpgrade {
				t.Errorf("upgrade hint = %v, want %v: %q", has, tt.wantUpgrade, got)
			}
			if !strings.Contains(got, tt.wantRedeploy) {
				t.Errorf("missing %q: %q", tt.wantRedeploy, got)
			}
		})
	}
}

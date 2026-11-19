package host

import (
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// An agent that can't take part in a volume copy is named before the
// migration stops anything.
func TestVolumeTransferError(t *testing.T) {
	agent := func(caps ...string) Host {
		return NewRemote("a1", "edge-1", &fakeLive{ag: &agents.ConnectedAgent{ID: "a1", Hello: agents.HelloPayload{Capabilities: caps}}})
	}
	current := []string{agents.CapStreamCredit, agents.CapVolumeStaged, agents.CapVolumeManage}
	tests := []struct {
		name    string
		h       Host
		receive bool
		want    error
	}{
		{"local host", &LocalHost{}, true, nil},
		{"current agent sends", agent(current...), false, nil},
		{"current agent receives", agent(current...), true, nil},
		{"agent without flow control", agent(agents.CapVolumeStaged, agents.CapVolumeManage), false, ErrAgentNoVolumeTransfer},
		{"agent without staged import", agent(agents.CapStreamCredit, agents.CapVolumeManage), true, ErrAgentNoVolumeTransfer},
		{"agent without volume create sends", agent(agents.CapStreamCredit), false, nil},
		{"agent without volume create receives", agent(agents.CapStreamCredit, agents.CapVolumeStaged), true, ErrAgentNoVolumeTransfer},
		{"offline agent", NewRemote("a1", "edge-1", &fakeLive{}), false, ErrAgentOffline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VolumeTransferError(tt.h, tt.receive); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

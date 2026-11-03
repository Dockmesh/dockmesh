package host

import (
	"context"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

type fakeLive struct{ ag *agents.ConnectedAgent }

func (f *fakeLive) GetConnected(string) *agents.ConnectedAgent { return f.ag }

func TestRemoteHostResolvesSessionPerCall(t *testing.T) {
	live := &fakeLive{}
	h := NewRemote("a1", "edge-1", live)
	if _, err := h.ListContainers(context.Background(), false); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline agent: got %v", err)
	}
	if _, err := h.ContainerLogs(context.Background(), "c1", "10", false); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline agent stream: got %v", err)
	}
	if _, _, err := h.ContainerExec(context.Background(), "c1", []string{"true"}); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline agent exec: got %v", err)
	}
}

// An agent without the volume frames gets a clear refusal instead of a
// request it can't answer.
func TestRemoteVolumeManageNeedsCapability(t *testing.T) {
	h := NewRemote("a1", "edge-1", &fakeLive{ag: &agents.ConnectedAgent{ID: "a1", Hello: agents.HelloPayload{Capabilities: []string{agents.CapContainerFiles}}}})
	if _, err := h.CreateVolume(context.Background(), "data", "", nil); !errors.Is(err, ErrAgentNoVolumeManage) {
		t.Errorf("create on an old agent: %v", err)
	}
	if err := h.RemoveVolume(context.Background(), "data", true); !errors.Is(err, ErrAgentNoVolumeManage) {
		t.Errorf("remove on an old agent: %v", err)
	}
}

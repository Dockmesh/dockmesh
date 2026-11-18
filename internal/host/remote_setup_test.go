package host

import (
	"context"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

type waitingLive struct {
	fakeLive
	err error
}

func (w *waitingLive) SetupError(context.Context, string) error { return w.err }

func TestDeployRefusedWhileWaitingForSetup(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"waiting", &agents.WaitingForSetupError{Name: "edge-1"}, agents.ErrWaitingForSetup},
		{"deleted meanwhile", agents.ErrNotFound, ErrAgentOffline},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// No session: a deploy that passes the setup check fails as
			// offline, without a frame.
			h := NewRemote("a1", "edge-1", &waitingLive{err: tc.err})
			_, err := h.DeployStack(context.Background(), "web", "services: {}\n", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("DeployStack = %v, want %v", err, tc.want)
			}
		})
	}
}

package host

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func TestRemoteSwarmServiceLogsNeedsCapability(t *testing.T) {
	h := swarmAgent(t, func(string) (any, error) { return nil, nil }, agents.CapSwarmNodes, agents.CapSwarmRead)
	if _, err := h.SwarmServiceLogs(context.Background(), agents.SwarmLogsReq{Service: "web"}); !errors.Is(err, ErrAgentNoSwarmLogs) {
		t.Fatalf("agent without %s: %v", agents.CapSwarmLogs, err)
	}
}

type endReader struct{ err error }

func (e endReader) Read([]byte) (int, error) { return 0, e.err }
func (endReader) Close() error               { return nil }

// The agent reports a refusal as the stream's close text; it comes back
// as the sentinel, other errors as they are.
func TestSwarmStreamMapsRefusals(t *testing.T) {
	other := errors.New("daemon went away")
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"worker", errors.New(agents.ErrNotSwarmManager.Error()), agents.ErrNotSwarmManager},
		{"gone", errors.New(agents.ErrSwarmServiceNotFound.Error()), agents.ErrSwarmServiceNotFound},
		{"other", other, other},
		{"eof", io.EOF, io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&swarmStream{ReadCloser: endReader{tt.in}}).Read(make([]byte, 8))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
		})
	}
}

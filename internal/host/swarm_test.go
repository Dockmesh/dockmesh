package host

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/swarmtask"
)

// An agent host inspects before it sends the restart frame: a Swarm task
// container is refused without a restart frame, a plain one restarts.
func TestRemoteRestartRefusesSwarmTask(t *testing.T) {
	labels := map[string]map[string]string{
		"task":  {swarmtask.LabelServiceID: "x1", swarmtask.LabelServiceName: "web"},
		"plain": nil,
	}
	var (
		mu        sync.Mutex
		restarted []string
	)
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion},
		func(fr agents.Frame, reply func(agents.Frame)) {
			var req agents.ContainerIDReq
			_ = json.Unmarshal(fr.Payload, &req)
			env := agents.ResponseEnvelope{OK: true}
			switch fr.Type {
			case agents.FrameReqContainerInspect:
				env.Data, _ = json.Marshal(container.InspectResponse{ID: req.ID, Config: &container.Config{Labels: labels[req.ID]}})
			case agents.FrameReqContainerRestart:
				mu.Lock()
				restarted = append(restarted, req.ID)
				mu.Unlock()
				env.Data = json.RawMessage(`{}`)
			default:
				env = agents.ResponseEnvelope{Error: "unexpected frame " + fr.Type}
			}
			payload, _ := json.Marshal(env)
			reply(agents.Frame{Type: agents.FrameRes, ID: fr.ID, Payload: payload})
		})
	t.Cleanup(stop)
	h := NewRemote("a1", "edge-1", &fakeLive{ag: ag})

	tests := []struct {
		id      string
		wantSvc string
	}{
		{"task", "web"},
		{"plain", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			mu.Lock()
			restarted = nil
			mu.Unlock()
			err := h.RestartContainer(context.Background(), tt.id)
			svc, refused := swarmtask.AsManaged(err)
			if svc != tt.wantSvc || (tt.wantSvc == "" && err != nil) {
				t.Fatalf("err %v (service %q, refused %v), want service %q", err, svc, refused, tt.wantSvc)
			}
			mu.Lock()
			defer mu.Unlock()
			if want := tt.wantSvc == ""; (len(restarted) == 1) != want {
				t.Fatalf("restart frames %v, want sent=%v", restarted, want)
			}
		})
	}
}

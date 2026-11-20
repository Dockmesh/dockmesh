package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// swarmAgent answers every Swarm frame with reply(frame type).
func swarmAgent(t *testing.T, reply func(string) (any, error), caps ...string) *RemoteHost {
	t.Helper()
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: caps},
		func(fr agents.Frame, send func(agents.Frame)) {
			data, err := reply(fr.Type)
			env := agents.ResponseEnvelope{OK: err == nil}
			if err != nil {
				env.Error = err.Error()
			} else {
				env.Data, _ = json.Marshal(data)
			}
			payload, _ := json.Marshal(env)
			send(agents.Frame{Type: agents.FrameRes, ID: fr.ID, Payload: payload})
		})
	t.Cleanup(stop)
	return NewRemote("a1", "m1", &fakeLive{ag: ag})
}

func TestRemoteSwarmReadErrors(t *testing.T) {
	ctx := context.Background()
	old := swarmAgent(t, func(string) (any, error) { return agents.SwarmServicesResp{}, nil }, agents.CapSwarmNodes)
	if _, err := swarmServices(ctx, old); !errors.Is(err, ErrAgentNoSwarmRead) {
		t.Fatalf("agent without the capability: %v", err)
	}
	tests := []struct {
		name  string
		agent error
		want  error
	}{
		{"worker", agents.ErrNotSwarmManager, agents.ErrNotSwarmManager},
		{"unknown service", agents.ErrSwarmServiceNotFound, agents.ErrSwarmServiceNotFound},
		{"other", errors.New("daemon down"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := swarmAgent(t, func(string) (any, error) { return nil, tt.agent }, agents.CapSwarmNodes, agents.CapSwarmRead)
			_, err := swarmService(ctx, h, "web")
			if err == nil {
				t.Fatal("no error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if tt.want == nil && (errors.Is(err, agents.ErrNotSwarmManager) || errors.Is(err, agents.ErrSwarmServiceNotFound)) {
				t.Fatalf("err %v mapped to a sentinel", err)
			}
			if _, err := h.SwarmNodes(ctx); errors.Is(tt.want, agents.ErrNotSwarmManager) && !errors.Is(err, tt.want) {
				t.Fatalf("nodes: %v", err)
			}
		})
	}
}

// The server sanitizes what an agent sends: env values, bad ids and lists
// beyond the bounds don't get through.
func TestRemoteSwarmReadSanitizes(t *testing.T) {
	ctx := context.Background()
	tasks := make([]map[string]any, 0, 6000)
	for i := range 6000 {
		tasks = append(tasks, map[string]any{"id": fmt.Sprintf("t%d", i), "service_id": "s1", "state": "running"})
	}
	h := swarmAgent(t, func(ft string) (any, error) {
		switch ft {
		case agents.FrameReqSwarmServiceInspect:
			return map[string]any{
				"id": "s1", "name": "web", "mode": "replicated",
				"env_keys": []string{"DB_PASSWORD=hunter2", "OK"},
				"secrets":  []map[string]any{{"id": "x", "name": "pw", "data": "hunter2"}},
				"spec":     map[string]any{"Env": []string{"A=hunter2"}},
			}, nil
		case agents.FrameReqSwarmTasks:
			return map[string]any{"tasks": tasks}, nil
		case agents.FrameReqSwarmNetworks:
			return map[string]any{"networks": []map[string]any{
				{"id": "o1", "name": "net", "subnets": []string{"10.0.0.0/24", "not a subnet"}},
				{"id": "BAD!", "name": "x"},
			}}, nil
		}
		return nil, errors.New("unexpected")
	}, agents.CapSwarmNodes, agents.CapSwarmRead)

	d, err := swarmService(ctx, h, "s1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("value got through: %s", b)
	}
	if len(d.EnvKeys) != 2 || d.EnvKeys[0] != "DB_PASSWORD" {
		t.Fatalf("env keys %v", d.EnvKeys)
	}

	tr, err := swarmTasks(ctx, h, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Tasks) != 5000 || !tr.Truncated {
		t.Fatalf("%d tasks, truncated %v", len(tr.Tasks), tr.Truncated)
	}

	nr, err := swarmNetworks(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(nr.Networks) != 1 || len(nr.Networks[0].Subnets) != 1 {
		t.Fatalf("networks %+v", nr.Networks)
	}
}

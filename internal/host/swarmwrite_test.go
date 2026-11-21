package host

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func writeReq() agents.SwarmDeployReq {
	ns := map[string]string{agents.LabelStackNamespace: "web"}
	return agents.SwarmDeployReq{
		Stack:    "web",
		Networks: []network.CreateRequest{{Name: "web_default", Driver: "overlay", Labels: ns}},
		Services: []agents.SwarmServiceWrite{{Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "web_app", Labels: map[string]string{
				agents.LabelStackNamespace: "web",
				agents.LabelSpecHash:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx", Labels: ns}},
		}}},
	}
}

func TestRemoteSwarmWriteErrors(t *testing.T) {
	ctx := context.Background()
	old := swarmAgent(t, func(string) (any, error) { return agents.SwarmDeployResp{}, nil }, agents.CapSwarmRead)
	if _, err := old.swarmDeploy(ctx, writeReq()); !errors.Is(err, ErrAgentNoSwarmWrite) {
		t.Fatalf("agent without the capability: %v", err)
	}
	tests := []struct {
		name  string
		agent error
		want  error
	}{
		{"worker", agents.ErrNotSwarmManager, agents.ErrNotSwarmManager},
		{"refused", fmt.Errorf("%w: service web_app exists and is not part of stack web", agents.ErrSwarmWriteRefused), agents.ErrSwarmWriteRefused},
		{"daemon error before writing", errors.New("read swarm state: down"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := swarmAgent(t, func(string) (any, error) { return nil, tt.agent }, agents.CapSwarmWrite)
			_, err := h.swarmDeploy(ctx, writeReq())
			if err == nil {
				t.Fatal("no error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if errors.Is(err, ErrSwarmWriteUnknown) {
				t.Fatalf("err %v: an answered error reported as unknown outcome", err)
			}
		})
	}
}

func TestRemoteSwarmDeployAnswer(t *testing.T) {
	want := agents.SwarmDeployResp{
		Services: []agents.SwarmWriteResult{{Name: "web_app", ID: "s1", Action: agents.SwarmActionCreated}},
		Error:    "web_db: no such image",
	}
	h := swarmAgent(t, func(string) (any, error) { return want, nil }, agents.CapSwarmWrite)
	got, err := h.swarmDeploy(context.Background(), writeReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].ID != "s1" || got.Error != want.Error {
		t.Fatalf("answer %+v", got)
	}
}

func TestSwarmWriteErrorWithoutAnswer(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("agent request %q: %w", agents.FrameReqSwarmStackDeploy, agents.ErrAgentGone),
		fmt.Errorf("agent request %q timed out: %w", agents.FrameReqSwarmStackDeploy, context.DeadlineExceeded),
		context.Canceled,
	} {
		if got := agentSwarmWriteError(err); !errors.Is(got, ErrSwarmWriteUnknown) {
			t.Errorf("%v → %v, want unknown outcome", err, got)
		}
	}
}

func TestRegistrySwarmDeployValidates(t *testing.T) {
	r := writeReq()
	r.Services[0].Spec.Name = "other_app"
	var reg Registry
	if _, err := reg.SwarmDeploy(context.Background(), "a1", r); !errors.Is(err, agents.ErrSwarmWriteRefused) {
		t.Fatalf("err %v, want a refusal before picking a host", err)
	}
	if _, err := reg.SwarmRemove(context.Background(), "a1", agents.SwarmRemoveReq{Stack: "web", Services: []string{"api_app"}}); !errors.Is(err, agents.ErrSwarmWriteRefused) {
		t.Fatalf("remove err %v, want a refusal before picking a host", err)
	}
}

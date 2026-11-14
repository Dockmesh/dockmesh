package host

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/topology"
)

func TestRemoteTopology(t *testing.T) {
	agent := func(caps []string) (*RemoteHost, *int) {
		asked := new(int)
		ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: caps},
			func(f agents.Frame, reply func(agents.Frame)) {
				if f.Type != agents.FrameReqNetworkTopology {
					return
				}
				*asked++
				data, _ := json.Marshal(topology.Graph{
					Networks:   []topology.Network{{ID: "n1", Name: "web_default", Stack: "web"}},
					Containers: []topology.Container{{ID: "c1", Name: "web-app-1", Stack: "web"}},
					Links:      []topology.Link{{NetworkID: "n1", ContainerID: "c1"}},
				})
				payload, _ := json.Marshal(agents.ResponseEnvelope{OK: true, Data: data})
				reply(agents.Frame{Type: agents.FrameRes, ID: f.ID, Payload: payload})
			})
		t.Cleanup(stop)
		return NewRemote("a1", "edge-1", &fakeLive{ag: ag}), asked
	}

	h, _ := agent([]string{agents.CapNetworkTopology})
	g, err := h.Topology(context.Background())
	if err != nil || len(g.Networks) != 1 || len(g.Containers) != 1 || len(g.Links) != 1 || g.Containers[0].Stack != "web" {
		t.Fatalf("topology = %+v, %v", g, err)
	}

	old, asked := agent(nil)
	if _, err := old.Topology(context.Background()); !errors.Is(err, ErrAgentNoTopology) {
		t.Fatalf("old agent: %v", err)
	}
	if *asked != 0 {
		t.Fatal("an agent without the capability was sent the frame")
	}
}

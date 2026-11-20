package agents

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/swarm"
)

func TestSanitizeSwarmNodes(t *testing.T) {
	in := []SwarmNode{
		{ID: "n1", Hostname: "m1.home.lab", Role: "manager", State: "ready", Leader: true, Reachability: "reachable"},
		{ID: "n1", Role: "manager"},                                    // duplicate id
		{ID: "N2!", Role: "worker"},                                    // bad id
		{ID: "n3", Role: "admin"},                                      // bad role
		{ID: "n4", Role: "worker", Leader: true, Hostname: "bad host"}, // worker can't lead, bad hostname
		{ID: strings.Repeat("a", 65), Role: "worker"},
	}
	got := SanitizeSwarmNodes(in)
	if len(got) != 2 || got[0].ID != "n1" || !got[0].Leader || got[0].Hostname != "m1.home.lab" {
		t.Fatalf("got %+v", got)
	}
	if got[1].ID != "n4" || got[1].Leader || got[1].Hostname != "" {
		t.Fatalf("worker entry %+v", got[1])
	}
}

func TestSwarmNodesFrom(t *testing.T) {
	nodes := []swarm.Node{
		{ID: "n1", Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive},
			Description: swarm.NodeDescription{Hostname: "m1"}, Status: swarm.NodeStatus{State: swarm.NodeStateReady},
			ManagerStatus: &swarm.ManagerStatus{Leader: true, Reachability: swarm.ReachabilityReachable}},
		{ID: "n2", Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}},
	}
	got := SwarmNodesFrom(nodes)
	want := []SwarmNode{
		{ID: "n1", Hostname: "m1", Role: "manager", State: "ready", Availability: "active", Leader: true, Reachability: "reachable"},
		{ID: "n2", Role: "worker"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
}

func TestSanitizeSwarmNodeDetails(t *testing.T) {
	got := SanitizeSwarmNodes([]SwarmNode{
		{ID: "n1", Role: "manager", Addr: "192.168.10.241", EngineVersion: "29.9.2", NanoCPUs: 2e9, MemoryBytes: 4 << 30},
		{ID: "n2", Role: "worker", Addr: "m1; rm -rf", EngineVersion: "29 <b>", NanoCPUs: -1, MemoryBytes: -1},
	})
	if got[0].Addr != "192.168.10.241" || got[0].EngineVersion != "29.9.2" || got[0].NanoCPUs != 2e9 {
		t.Fatalf("manager %+v", got[0])
	}
	if got[1].Addr != "" || got[1].EngineVersion != "" || got[1].NanoCPUs != 0 || got[1].MemoryBytes != 0 {
		t.Fatalf("worker %+v", got[1])
	}
}

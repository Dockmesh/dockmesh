package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func distNodes() []agents.SwarmNode {
	return []agents.SwarmNode{
		{ID: "n1", Hostname: "m1", Role: "manager", Leader: true, State: "ready", Availability: "active"},
		{ID: "n2", Hostname: "m2", Role: "manager", State: "ready", Availability: "active"},
		{ID: "n3", Hostname: "m3", Role: "manager", State: "ready", Availability: "active"},
		{ID: "n4", Hostname: "w1", Role: "worker", State: "ready", Availability: "active"},
	}
}

// tasksOn returns count running tasks of service on node, plus one
// failed task that must not count.
func tasksOn(service, node string, count int) []agents.SwarmTask {
	out := []agents.SwarmTask{{ServiceID: service, NodeID: node, State: "failed", DesiredState: "shutdown"}}
	for range count {
		out = append(out, agents.SwarmTask{ServiceID: service, NodeID: node, State: "running", DesiredState: "running"})
	}
	return out
}

func spread(counts ...int) []agents.SwarmTask {
	var out []agents.SwarmTask
	for i, c := range counts {
		out = append(out, tasksOn("web", distNodes()[i].ID, c)...)
	}
	return out
}

func TestDistributeHeavyRule(t *testing.T) {
	cases := []struct {
		name   string
		counts []int
		nodes  func([]agents.SwarmNode)
		heavy  []string
		fair   float64
		total  int
	}{
		{"even", []int{4, 4, 4, 4}, nil, nil, 4, 16},
		// fair 7.75: 14 is ≥ 1.5× and ≥ 3 above.
		{"one heavy node", []int{14, 6, 7, 4}, nil, []string{"n1"}, 7.75, 31},
		// fair 1.5: 3 is 2× but only 1.5 above.
		{"small numbers", []int{3, 1, 1, 1}, nil, nil, 1.5, 6},
		// fair 21.5: 25 is 3.5 above but below 1.5×.
		{"large numbers", []int{25, 20, 20, 21}, nil, nil, 21.5, 86},
		{"drained node does not count", []int{9, 3, 3, 0}, func(n []agents.SwarmNode) { n[3].Availability = "drain" }, []string{"n1"}, 5, 15},
		{"paused node does not count", []int{9, 3, 3, 0}, func(n []agents.SwarmNode) { n[3].Availability = "pause" }, []string{"n1"}, 5, 15},
		// Without the down node the fair share is 5 and m1 is heavy; its
		// stale tasks count nowhere.
		{"down node does not count", []int{9, 3, 3, 7}, func(n []agents.SwarmNode) { n[3].State = "down" }, []string{"n1"}, 5, 15},
		{"no tasks", []int{0, 0, 0, 0}, nil, nil, 0, 0},
		{"no node counts", []int{2, 0, 0, 0}, func(n []agents.SwarmNode) {
			for i := range n {
				n[i].Availability = "drain"
			}
		}, nil, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := distNodes()
			if tc.nodes != nil {
				tc.nodes(nodes)
			}
			d := distribute(distInput{nodes: nodes, tasks: spread(tc.counts...)})
			var heavy []string
			for _, n := range d.Nodes {
				if n.Heavy {
					heavy = append(heavy, n.ID)
				}
			}
			if !reflect.DeepEqual(heavy, tc.heavy) || d.FairShare != tc.fair || d.Tasks != tc.total {
				t.Fatalf("heavy %v fair %v tasks %d, want %v %v %d", heavy, d.FairShare, d.Tasks, tc.heavy, tc.fair, tc.total)
			}
			for i, n := range d.Nodes {
				if n.Tasks != tc.counts[i] {
					t.Fatalf("node %s tasks %d, want %d", n.ID, n.Tasks, tc.counts[i])
				}
			}
		})
	}
}

// The heavy node names the tasks a placement constraint pins to it, and
// only to callers who see services.
func TestDistributePinned(t *testing.T) {
	tasks := append(spread(10, 4, 4, 4), tasksOn("db", "n1", 1)...)
	tasks = append(tasks, tasksOn("cache", "n1", 2)...)
	tasks = append(tasks, tasksOn("api", "n1", 1)...)
	services := []agents.SwarmService{
		{ID: "web", Name: "shop_web"},
		{ID: "db", Name: "shop_db", Constraints: []string{"node.hostname == m1"}},
		{ID: "cache", Name: "shop_cache", Constraints: []string{"node.role==manager", "node.id==n1"}},
		{ID: "api", Name: "shop_api", Constraints: []string{"node.hostname != m1"}},
	}
	d := distribute(distInput{nodes: distNodes(), tasks: tasks, services: services})
	want := []distPinned{{Service: "shop_cache", Tasks: 2}, {Service: "shop_db", Tasks: 1}}
	if !d.Nodes[0].Heavy || !reflect.DeepEqual(d.Nodes[0].Pinned, want) {
		t.Fatalf("m1 %+v", d.Nodes[0])
	}
	if d := distribute(distInput{nodes: distNodes(), tasks: tasks}); d.Nodes[0].Pinned != nil || d.Services != nil {
		t.Fatalf("without services: %+v %+v", d.Nodes[0], d.Services)
	}
}

func TestPinnedTo(t *testing.T) {
	n := agents.SwarmNode{ID: "abc", Hostname: "m1"}
	cases := []struct {
		c    string
		want bool
	}{
		{"node.hostname==m1", true},
		{" node.hostname == m1 ", true},
		{"node.id==abc", true},
		{"node.hostname==m10", false},
		{"node.hostname!=m1", false},
		{"node.labels.zone==m1", false},
		{"node.role==manager", false},
	}
	for _, tc := range cases {
		if got := pinnedTo([]string{tc.c}, n); got != tc.want {
			t.Errorf("%q: %v", tc.c, got)
		}
	}
}

func TestDistributeMetrics(t *testing.T) {
	nodes := distNodes()
	nodes[2].State = "down"
	in := distInput{
		nodes:  nodes,
		tasks:  append(spread(2, 1, 0, 1), tasksOn("db", "n2", 1)...),
		hostOf: map[string]string{"n1": "a1", "n2": "a2", "n3": "a3"},
		usage:  map[string]nodeUsage{"n1": {cpu: 71, mem: 68}},
		// n2's agent is offline; n4 has none.
		offline:  map[string]bool{"n2": true},
		services: []agents.SwarmService{{ID: "web", Name: "shop_web", Stack: "shop"}, {ID: "db", Name: "shop_db", Stack: "shop"}, {ID: "idle", Name: "idle"}},
		measured: map[string]map[string]svcUsage{"n1": {"web": {cores: 0.5, mem: 300}}},
	}
	d := distribute(in)
	got := map[string]string{}
	for _, n := range d.Nodes {
		got[n.ID] = n.NoMetrics
	}
	want := map[string]string{"n1": "", "n2": noMetricsAgentOffline, "n3": noMetricsUnreachable, "n4": noMetricsNoAgent}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no_metrics %v", got)
	}
	if n1 := d.Nodes[0]; n1.CPUPercent == nil || *n1.CPUPercent != 71 || *n1.MemPercent != 68 || n1.HostID != "a1" {
		t.Fatalf("n1 %+v", n1)
	}
	if d.Nodes[3].HostID != "" || d.Nodes[3].CPUPercent != nil {
		t.Fatalf("n4 %+v", d.Nodes[3])
	}

	byName := map[string]distService{}
	for _, s := range d.Services {
		byName[s.Name] = s
	}
	web := byName["shop_web"]
	if web.Running != 4 || !reflect.DeepEqual(web.PerNode, map[string]int{"n1": 2, "n2": 1, "n4": 1}) || !web.Partial ||
		web.CPUCores == nil || *web.CPUCores != 0.5 || *web.MemBytes != 300 {
		t.Fatalf("web %+v", web)
	}
	// Every task of db runs where nothing was measured: no numbers at all.
	if db := byName["shop_db"]; !db.Partial || db.CPUCores != nil || db.MemBytes != nil || db.Running != 1 {
		t.Fatalf("db %+v", db)
	}
	if idle := byName["idle"]; idle.Running != 0 || idle.Partial || idle.CPUCores != nil || len(idle.PerNode) != 0 {
		t.Fatalf("idle %+v", idle)
	}

	in.measured = nil
	if web := distribute(in).Services[0]; web.Partial || web.CPUCores != nil {
		t.Fatalf("without usage %+v", web)
	}
}

// A cluster role covers the Swarm, not its node hosts: hosts the caller
// can't view are never asked, and their nodes say so.
func TestMeasureNodesNeedsHostAccess(t *testing.T) {
	nodes := distNodes()
	nodes[2].State = "down"
	in := distInput{nodes: nodes, hostOf: map[string]string{"n1": "a1", "n2": "a2", "n3": "a3"}}
	asked := map[string]bool{}
	(&Handlers{}).measureNodes(context.Background(), &in, true, func(hostID string) bool {
		asked[hostID] = true
		return hostID == "a1"
	})
	if !reflect.DeepEqual(asked, map[string]bool{"a1": true, "a2": true}) {
		t.Fatalf("access checked for %v", asked)
	}
	if !reflect.DeepEqual(in.denied, map[string]bool{"n2": true}) || !in.offline["n1"] || in.offline["n2"] {
		t.Fatalf("denied %v offline %v", in.denied, in.offline)
	}
	got := map[string]string{}
	for _, n := range distribute(in).Nodes {
		got[n.ID] = n.NoMetrics
	}
	want := map[string]string{"n1": noMetricsAgentOffline, "n2": noMetricsNoAccess, "n3": noMetricsUnreachable, "n4": noMetricsNoAgent}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no_metrics %v", got)
	}
}

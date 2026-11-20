package clusters

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/host"
)

const swarmID = "clusterabc123"

// fakeHosts serves daemon info and node lists per host id.
type fakeHosts struct {
	mu      sync.Mutex
	infos   map[string]*agents.DaemonInfo
	failing map[string]bool // CurrentDaemonInfo fails (host unreadable)
	nodes   map[string][]agents.SwarmNode
	asked   []string // SwarmNodes calls
}

func (f *fakeHosts) List(context.Context) ([]host.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.Info
	for id := range f.infos {
		out = append(out, host.Info{ID: id, Name: "h-" + id})
	}
	for id := range f.failing {
		if _, ok := f.infos[id]; !ok {
			out = append(out, host.Info{ID: id, Name: "h-" + id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeHosts) DaemonInfo(_ context.Context, id string) (*agents.DaemonInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infos[id], nil
}

func (f *fakeHosts) CurrentDaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error) {
	f.mu.Lock()
	failing := f.failing[id]
	f.mu.Unlock()
	if failing {
		return nil, errors.New("offline")
	}
	return f.DaemonInfo(ctx, id)
}

func (f *fakeHosts) SwarmNodes(_ context.Context, id string) ([]agents.SwarmNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id)
	n, ok := f.nodes[id]
	if !ok {
		return nil, errors.New("not a manager")
	}
	return n, nil
}

func swarmInfo(cluster, node, role string) *agents.DaemonInfo {
	return &agents.DaemonInfo{OS: "linux", Arch: "amd64", Swarm: &agents.SwarmState{NodeID: node, NodeState: "active", Role: role, ClusterID: cluster}}
}

// lab: m1, m2 managers, w1 worker, solo outside the Swarm, and a node
// "w2" that has no agent (only in the node list).
func lab() *fakeHosts {
	nodes := []agents.SwarmNode{
		{ID: "n1", Role: RoleManager, Leader: true},
		{ID: "n2", Role: RoleManager},
		{ID: "n3", Role: RoleWorker},
		{ID: "n4", Role: RoleWorker},
	}
	return &fakeHosts{
		infos: map[string]*agents.DaemonInfo{
			"m1":   swarmInfo(swarmID, "n1", RoleManager),
			"m2":   swarmInfo(swarmID, "n2", RoleManager),
			"w1":   swarmInfo(swarmID, "n3", RoleWorker),
			"solo": {OS: "linux", Swarm: &agents.SwarmState{NodeState: "inactive"}},
		},
		failing: map[string]bool{},
		nodes:   map[string][]agents.SwarmNode{"m1": nodes, "m2": nodes},
	}
}

func newService(t *testing.T, f *fakeHosts) *Service {
	t.Helper()
	db, err := dbtest.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, f)
}

func roles(c Cluster) map[string]string {
	out := map[string]string{}
	for _, m := range c.Members {
		out[m.HostID] = m.NodeID + "/" + m.Role
	}
	return out
}

func equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestCreateLinksConfirmedNodes(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)

	cands, err := s.Candidates(ctx)
	if err != nil || len(cands) != 1 || cands[0].ClusterID != swarmID || len(cands[0].Managers) != 2 {
		t.Fatalf("candidates %+v %v", cands, err)
	}
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}
	if c.ID != swarmID || c.Name != "swarm-clustera" || !equal(roles(c), want) {
		t.Fatalf("created %+v", c)
	}
	if cands, _ := s.Candidates(ctx); len(cands) != 0 {
		t.Fatalf("candidates after create %+v", cands)
	}
	if _, err := s.Create(ctx, "m2", "other", "u1"); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v", err)
	}
}

func TestCreateRefuses(t *testing.T) {
	tests := []struct {
		name  string
		host  string
		cname string
		setup func(f *fakeHosts)
		want  error
	}{
		{"worker", "w1", "", nil, ErrNotManager},
		{"not in a swarm", "solo", "", nil, ErrNotManager},
		{"claims manager, node list says worker", "m2", "", func(f *fakeHosts) {
			f.nodes["m2"] = []agents.SwarmNode{{ID: "n1", Role: RoleManager}, {ID: "n2", Role: RoleWorker}}
		}, ErrNotManager},
		{"claims manager, own node missing", "m2", "", func(f *fakeHosts) {
			f.nodes["m2"] = []agents.SwarmNode{{ID: "n1", Role: RoleManager}}
		}, ErrNotManager},
		{"bad name", "m1", "../x", nil, ErrBadName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := lab()
			if tt.setup != nil {
				tt.setup(f)
			}
			s := newService(t, f)
			if _, err := s.Create(context.Background(), tt.host, tt.cname, ""); !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if all, _ := s.List(context.Background()); len(all) != 0 {
				t.Fatalf("cluster stored: %+v", all)
			}
		})
	}
}

// A host claiming a cluster's id and a node ID the managers don't list is
// never linked, and neither is a second host claiming a linked node ID.
func TestClaimsAloneDontLink(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	f.infos["fake"] = swarmInfo(swarmID, "n9", RoleManager)
	f.infos["clone"] = swarmInfo(swarmID, "n3", RoleWorker)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.get(ctx, swarmID)
	if want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}; !equal(roles(c), want) {
		t.Fatalf("members %v", roles(c))
	}
}

// Reconcile follows the managers' node list; an unreadable host keeps its
// membership, a host that left or was removed loses it, and running it
// twice without a change writes nothing.
func TestReconcile(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	before, _ := s.get(ctx, swarmID)

	for i := range 2 {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		after, _ := s.get(ctx, swarmID)
		if !equal(roles(after), roles(before)) {
			t.Fatalf("run %d changed members: %v → %v", i, roles(before), roles(after))
		}
		for j := range after.Members {
			if !after.Members[j].LinkedAt.Equal(before.Members[j].LinkedAt) {
				t.Fatalf("run %d rewrote %s", i, after.Members[j].HostID)
			}
		}
	}

	// Origin manager gone: m2 answers. w1 unreadable: kept. m2 promoted
	// w1 → manager in the list.
	f.failing["m1"] = true
	f.failing["w1"] = true
	f.nodes["m2"] = []agents.SwarmNode{{ID: "n1", Role: RoleManager}, {ID: "n2", Role: RoleManager}, {ID: "n3", Role: RoleManager}}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.get(ctx, swarmID)
	if want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}; !equal(roles(c), want) {
		t.Fatalf("unreadable hosts changed: %v", roles(c))
	}

	// w1 readable again and listed as manager; m1 left the Swarm.
	delete(f.failing, "w1")
	delete(f.failing, "m1")
	f.infos["m1"] = &agents.DaemonInfo{Swarm: &agents.SwarmState{NodeState: "inactive"}}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ = s.get(ctx, swarmID)
	if want := map[string]string{"m2": "n2/manager", "w1": "n3/manager"}; !equal(roles(c), want) {
		t.Fatalf("after leave: %v", roles(c))
	}

	// w1's agent is removed from Dockmesh.
	delete(f.infos, "w1")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ = s.get(ctx, swarmID)
	if want := map[string]string{"m2": "n2/manager"}; !equal(roles(c), want) {
		t.Fatalf("after remove: %v", roles(c))
	}
}

// Without a manager that answers, the last known members stay.
func TestReconcileNoManagerKeepsMembers(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	f.nodes = map[string][]agents.SwarmNode{}
	delete(f.infos, "w1")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.get(ctx, swarmID)
	if len(c.Members) != 3 {
		t.Fatalf("members %v", roles(c))
	}
}

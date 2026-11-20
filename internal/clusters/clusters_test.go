package clusters

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

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
	status  map[string]string
	nodes   map[string][]agents.SwarmNode
	// roots is the trust root each manager host reports.
	roots map[string]string
	// provers answer SwarmProve per host; a host without one has an agent
	// too old to prove.
	provers map[string]func(agents.SwarmProveReq) (agents.SwarmProveResp, error)
	proved  map[string]int
	joined  map[string]time.Time
	peers   map[string]string
}

func (f *fakeHosts) List(context.Context) ([]host.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.Info
	for id := range f.infos {
		out = append(out, host.Info{ID: id, Name: "h-" + id, Status: f.status[id]})
	}
	for id := range f.failing {
		if _, ok := f.infos[id]; !ok {
			out = append(out, host.Info{ID: id, Name: "h-" + id, Status: f.status[id]})
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
	n, ok := f.nodes[id]
	if !ok {
		return nil, errors.New("not a manager")
	}
	return n, nil
}

func (f *fakeHosts) SwarmNodeList(ctx context.Context, id string) (agents.SwarmNodesResp, error) {
	nodes, err := f.SwarmNodes(ctx, id)
	if err != nil {
		return agents.SwarmNodesResp{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return agents.SwarmNodesResp{Nodes: nodes, TrustRoot: f.roots[id]}, nil
}

func (f *fakeHosts) SwarmProve(_ context.Context, id string, req agents.SwarmProveReq) (agents.SwarmProveResp, error) {
	f.mu.Lock()
	p := f.provers[id]
	if f.proved == nil {
		f.proved = map[string]int{}
	}
	f.proved[id]++
	f.mu.Unlock()
	if p == nil {
		return agents.SwarmProveResp{}, host.ErrAgentNoSwarmProve
	}
	return p(req)
}

// JoinedAt: hosts are online since long ago unless marked offline or
// failing, or given a session start in joined.
func (f *fakeHosts) JoinedAt(id string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.joined[id]; ok {
		return t
	}
	if f.status[id] == "offline" || f.failing[id] {
		return time.Time{}
	}
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
}

func (f *fakeHosts) RemoteIP(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peers[id]
}

func (f *fakeHosts) provedCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proved[id]
}

func swarmInfo(cluster, node, role string) *agents.DaemonInfo {
	return &agents.DaemonInfo{OS: "linux", Arch: "amd64", Swarm: &agents.SwarmState{NodeID: node, NodeState: "active", Role: role, ClusterID: cluster}}
}

// lab: m1, m2 managers, w1 worker, solo outside the Swarm, and node "n4"
// that has no agent (only in the node list). Like dockerd, the worker
// reports no cluster ID.
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
			"w1":   swarmInfo("", "n3", RoleWorker),
			"solo": {OS: "linux", Swarm: &agents.SwarmState{NodeState: "inactive"}},
		},
		failing: map[string]bool{},
		status:  map[string]string{},
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

type auditRec struct{ got []string }

func (a *auditRec) Write(_ context.Context, _, action, _ string, details any) {
	h, _ := details.(map[string]any)["host"].(string)
	a.got = append(a.got, action+" "+h)
}

func backdate(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE cluster_memberships SET linked_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
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

// A host claiming the cluster with a node ID the origin doesn't list is
// never linked, nor is a second host claiming a linked node ID, nor a
// manager-looking worker claim without the cluster ID.
func TestClaimsAloneDontLink(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	f.infos["fake"] = swarmInfo(swarmID, "n9", RoleManager)
	f.infos["clone"] = swarmInfo("", "n3", RoleWorker)
	f.infos["nocluster"] = swarmInfo("", "n4", RoleManager)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.get(ctx, swarmID)
	if want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}; !equal(roles(c), want) {
		t.Fatalf("members %v", roles(c))
	}
}

// Only the origin's node list changes memberships: another linked
// manager's list is never used, so it can't unlink anyone.
func TestOnlyOriginListCounts(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}
	f.nodes["m2"] = []agents.SwarmNode{{ID: "n2", Role: RoleManager}}
	tests := []struct {
		name  string
		setup func()
	}{
		{"origin unreadable", func() { f.failing["m1"] = true }},
		{"origin left the swarm", func() {
			delete(f.failing, "m1")
			f.infos["m1"] = &agents.DaemonInfo{Swarm: &agents.SwarmState{NodeState: "inactive"}}
		}},
		{"origin reports another node", func() { f.infos["m1"] = swarmInfo(swarmID, "n2", RoleManager) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			c, _ := s.get(ctx, swarmID)
			if !equal(roles(c), want) {
				t.Fatalf("members changed: %v", roles(c))
			}
		})
	}
}

// Reconcile follows the origin's node list; an unreadable host keeps its
// membership, a host that left, was revoked or removed loses it, and
// running it twice without a change writes nothing.
func TestReconcile(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	rec := &auditRec{}
	s.SetAudit(rec)
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	sort.Strings(rec.got)
	if want := []string{"cluster.member_link m1", "cluster.member_link m2", "cluster.member_link w1"}; !slices.Equal(rec.got, want) {
		t.Fatalf("audit %v", rec.got)
	}
	rec.got = nil
	backdate(t, s)
	before, _ := s.get(ctx, swarmID)
	for i := range 2 {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		after, _ := s.get(ctx, swarmID)
		if !equal(roles(after), roles(before)) {
			t.Fatalf("run %d changed members: %v → %v", i, roles(before), roles(after))
		}
		for _, m := range after.Members {
			if m.LinkedAt.Year() != 2000 {
				t.Fatalf("run %d rewrote %s", i, m.HostID)
			}
		}
	}
	if len(rec.got) != 0 {
		t.Fatalf("unchanged runs audited %v", rec.got)
	}

	f.failing["w1"] = true
	f.nodes["m1"] = []agents.SwarmNode{{ID: "n1", Role: RoleManager}, {ID: "n2", Role: RoleWorker}, {ID: "n3", Role: RoleWorker}}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.get(ctx, swarmID)
	if want := map[string]string{"m1": "n1/manager", "m2": "n2/worker", "w1": "n3/worker"}; !equal(roles(c), want) {
		t.Fatalf("after demote: %v", roles(c))
	}

	delete(f.failing, "w1")
	f.infos["m2"] = &agents.DaemonInfo{Swarm: &agents.SwarmState{NodeState: "inactive"}}
	f.status["w1"] = "revoked"
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ = s.get(ctx, swarmID)
	if want := map[string]string{"m1": "n1/manager"}; !equal(roles(c), want) {
		t.Fatalf("after leave and revoke: %v", roles(c))
	}
	if !slices.Contains(rec.got, "cluster.member_unlink w1") || !slices.Contains(rec.got, "cluster.member_unlink m2") {
		t.Fatalf("unlinks not audited: %v", rec.got)
	}
}

// Create refuses when another manager host of the Swarm doesn't list the
// chosen host as a manager.
func TestCreateConflict(t *testing.T) {
	ctx := context.Background()
	f := lab()
	f.nodes["m2"] = []agents.SwarmNode{{ID: "n2", Role: RoleManager}}
	s := newService(t, f)
	if _, err := s.Create(ctx, "m1", "prod", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("err %v", err)
	}
	delete(f.nodes, "m2") // an unreachable manager doesn't block
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := newService(t, lab())
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	if c, _, err := s.Delete(ctx, swarmID, nil); err != nil || len(c.Members) != 3 {
		t.Fatalf("delete: %+v %v", c, err)
	}
	if _, _, err := s.Delete(ctx, swarmID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM cluster_memberships`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("memberships left: %d %v", n, err)
	}
	if cands, _ := s.Candidates(ctx); len(cands) != 1 {
		t.Fatalf("candidates after delete %+v", cands)
	}
}

// Without the origin answering, the last known members stay.
func TestReconcileNoOriginKeepsMembers(t *testing.T) {
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

// A host claiming the origin's own node can't keep the origin unlinked.
func TestOriginWinsTie(t *testing.T) {
	ctx := context.Background()
	f := lab()
	f.infos["imposter"] = swarmInfo(swarmID, "n1", RoleManager)
	f.nodes["imposter"] = f.nodes["m1"]
	s := newService(t, f)
	c, err := s.Create(ctx, "m1", "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(c); got["m1"] != "n1/manager" || got["imposter"] != "" {
		t.Fatalf("members %v", got)
	}
}

func TestConflictNamesHost(t *testing.T) {
	f := lab()
	f.nodes["m2"] = []agents.SwarmNode{{ID: "n2", Role: RoleManager}}
	_, err := newService(t, f).Create(context.Background(), "m1", "", "")
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Host != "m2" {
		t.Fatalf("err %v", err)
	}
}

// A cluster is read through its origin only while the origin is the
// linked manager node of this Swarm right now.
func TestReadHost(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		set  func()
		want error
	}{
		{"origin is the manager", func() {}, nil},
		{"origin offline", func() { f.failing["m1"] = true }, ErrUnreadable},
		{"origin demoted", func() { f.infos["m1"] = swarmInfo("", "n1", RoleWorker) }, ErrUnreadable},
		{"origin left the swarm", func() { f.infos["m1"] = &agents.DaemonInfo{Swarm: &agents.SwarmState{NodeState: "inactive"}} }, ErrUnreadable},
		{"origin claims another node", func() { f.infos["m1"] = swarmInfo(swarmID, "n9", RoleManager) }, ErrUnreadable},
		{"origin in another swarm", func() { f.infos["m1"] = swarmInfo("other", "n1", RoleManager) }, ErrUnreadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.mu.Lock()
			f.infos["m1"] = swarmInfo(swarmID, "n1", RoleManager)
			f.failing = map[string]bool{}
			tt.set()
			f.mu.Unlock()
			got, err := s.ReadHost(ctx, c.ID)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if err == nil && got != "m1" {
				t.Fatalf("read host %q", got)
			}
		})
	}
	if _, err := s.ReadHost(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown cluster: %v", err)
	}
}

func TestManagerOf(t *testing.T) {
	ctx := context.Background()
	s := newService(t, lab())
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"m1": c.ID, "m2": c.ID, "w1": "", "solo": "", "nope": ""} {
		got, err := s.ManagerOf(ctx, host)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", host, got, err, want)
		}
	}
}

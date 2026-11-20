package clusters

import (
	"context"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// nodeErrHosts fails the node list of the hosts in errs with that error.
type nodeErrHosts struct {
	*fakeHosts
	errs map[string]error
}

func (f *nodeErrHosts) SwarmNodes(ctx context.Context, id string) ([]agents.SwarmNode, error) {
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	return f.fakeHosts.SwarmNodes(ctx, id)
}

func readyNodes() []agents.SwarmNode {
	return []agents.SwarmNode{
		{ID: "n1", Hostname: "m1", Role: RoleManager, Leader: true, State: "ready", Availability: "active", Reachability: "reachable"},
		{ID: "n2", Hostname: "m2", Role: RoleManager, State: "ready", Availability: "active", Reachability: "reachable"},
		{ID: "n3", Hostname: "w1", Role: RoleWorker, State: "ready", Availability: "active"},
		{ID: "n4", Hostname: "w2", Role: RoleWorker, State: "ready", Availability: "drain"},
	}
}

func onlyCluster(t *testing.T, s *Service) Cluster {
	t.Helper()
	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	return list[0]
}

func TestClusterStatus(t *testing.T) {
	cases := []struct {
		name string
		// after runs between create and the reconcile.
		after           func(f *fakeHosts, errs map[string]error)
		state           string
		managers, reach int
		down, noAgent   int
		listFresh       bool
	}{
		{"healthy", func(*fakeHosts, map[string]error) {}, StateHealthy, 2, 2, 0, 1, true},
		{"manager unreachable", func(f *fakeHosts, _ map[string]error) {
			n := readyNodes()
			n[1].State, n[1].Reachability = "down", "unreachable"
			f.nodes["m1"] = n
		}, StateDegraded, 2, 1, 1, 1, true},
		{"worker down", func(f *fakeHosts, _ map[string]error) {
			n := readyNodes()
			n[3].State = "down"
			f.nodes["m1"] = n
		}, StateDegraded, 2, 2, 1, 1, true},
		{"origin lost the cluster", func(f *fakeHosts, _ map[string]error) {
			f.infos["m1"] = swarmInfo("", "n1", RoleManager)
		}, StateNoQuorum, 2, 2, 0, 1, false},
		{"no leader", func(_ *fakeHosts, errs map[string]error) {
			errs["m1"] = errors.New("Error response from daemon: rpc error: code = Unknown desc = The swarm does not have a leader. It's possible that too few managers are online.")
		}, StateNoQuorum, 2, 2, 0, 1, false},
		{"origin offline", func(f *fakeHosts, _ map[string]error) {
			f.failing["m1"] = true
		}, StateUnreachable, 2, 2, 0, 1, false},
		{"origin list fails", func(_ *fakeHosts, errs map[string]error) {
			errs["m1"] = errors.New("agent timeout")
		}, StateUnreachable, 2, 2, 0, 1, false},
		{"origin in another Swarm", func(f *fakeHosts, _ map[string]error) {
			f.infos["m1"] = swarmInfo("other", "n1", RoleManager)
		}, StateUnreachable, 2, 2, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := lab()
			f.nodes["m1"], f.nodes["m2"] = readyNodes(), readyNodes()
			errs := map[string]error{}
			s := newService(t, f)
			s.hosts = &nodeErrHosts{fakeHosts: f, errs: errs}
			ctx := context.Background()
			if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
				t.Fatal(err)
			}
			created := onlyCluster(t, s).Status
			if created.State != StateHealthy || created.KnownAt == nil {
				t.Fatalf("after create: %+v", created)
			}
			f.mu.Lock()
			tc.after(f, errs)
			f.mu.Unlock()
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			st := onlyCluster(t, s).Status
			if st.State != tc.state || st.Managers != tc.managers || st.ManagersReachable != tc.reach ||
				st.NodesDown != tc.down || st.NodesWithoutAgent != tc.noAgent || st.Workers != 2 || st.Leader != "m1" {
				t.Fatalf("status = %+v", st)
			}
			if st.CheckedAt == nil || st.KnownAt == nil {
				t.Fatalf("times missing: %+v", st)
			}
			if fresh := !st.KnownAt.Before(*st.CheckedAt) && st.KnownAt.After(*created.KnownAt); fresh != tc.listFresh {
				t.Fatalf("node list fresh = %v, want %v (known %v, checked %v)", fresh, tc.listFresh, st.KnownAt, st.CheckedAt)
			}
		})
	}
}

func TestClusterStatusUnknownUntilAsked(t *testing.T) {
	f := lab()
	s := newService(t, f)
	ctx := context.Background()
	if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.db, f)
	if st := onlyCluster(t, restarted).Status; st.State != StateUnknown || st.CheckedAt != nil {
		t.Fatalf("status before the first reconcile = %+v", st)
	}
}

func TestDeleteForgetsStatus(t *testing.T) {
	f := lab()
	s := newService(t, f)
	ctx := context.Background()
	if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Delete(ctx, swarmID, func(Access) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.seenOf(swarmID); ok {
		t.Fatal("status kept after delete")
	}
}

func TestMemberships(t *testing.T) {
	f := lab()
	f.nodes["m1"], f.nodes["m2"] = readyNodes(), readyNodes()
	s := newService(t, f)
	ctx := context.Background()
	if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.Memberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Membership{}
	for _, m := range list {
		got[m.HostID] = m
	}
	if len(got) != 3 {
		t.Fatalf("memberships = %+v", list)
	}
	m1 := got["m1"]
	if m1.ClusterName != "lab" || m1.ClusterID != swarmID || m1.Role != RoleManager || m1.Node == nil || !m1.Node.Leader || m1.Node.Reachability != "reachable" {
		t.Fatalf("m1 = %+v %+v", m1, m1.Node)
	}
	if w := got["w1"]; w.Role != RoleWorker || w.Node == nil || w.Node.State != "ready" || w.Node.Leader {
		t.Fatalf("w1 = %+v %+v", w, w.Node)
	}

	restarted := New(s.db, f)
	list, err = restarted.Memberships(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("after restart: %v %v", list, err)
	}
	for _, m := range list {
		if m.Node != nil {
			t.Fatalf("node state before the first reconcile: %+v", m)
		}
	}
}

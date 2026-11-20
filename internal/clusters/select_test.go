package clusters

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func setOffline(f *fakeHosts, host string, off bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off {
		f.status[host] = "offline"
		f.failing[host] = true
	} else {
		delete(f.status, host)
		delete(f.failing, host)
	}
}

func readNow(t *testing.T, s *Service) (string, error) {
	t.Helper()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s.ReadHost(context.Background(), swarmID)
}

// Reads move to the next verified manager when the current one goes away
// and stay there when it comes back.
func TestFailoverIsSticky(t *testing.T) {
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name    string
		offline map[string]bool
		want    string
		state   string
	}{
		{"both up: the leader m1", map[string]bool{}, "m1", StateDegraded},
		{"m1 gone: m2 takes over", map[string]bool{"m1": true}, "m2", StateDegraded},
		{"m1 back: m2 stays", map[string]bool{"m1": false}, "m2", StateDegraded},
		{"m2 gone: back to m1", map[string]bool{"m2": true}, "m1", StateDegraded},
		{"both gone: unreachable", map[string]bool{"m1": true, "m2": true}, "", StateUnreachable},
	}
	for _, st := range steps {
		for h, off := range st.offline {
			setOffline(f, h, off)
		}
		got, err := readNow(t, s)
		if st.want == "" {
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("%s: read host %q, err %v", st.name, got, err)
			}
		} else if err != nil || got != st.want {
			t.Fatalf("%s: read host %q, err %v, want %s", st.name, got, err, st.want)
		}
		c, _ := s.Get(context.Background(), swarmID)
		if c.Status.State != st.state || (st.want != "" && c.Status.ReadHost != st.want) {
			t.Fatalf("%s: status %+v", st.name, c.Status)
		}
	}
}

// Without a reader yet, the leader is preferred when it has a verified
// agent.
func TestLeaderPreferred(t *testing.T) {
	f, _ := provedLab(t)
	nodes := slices.Clone(f.nodes["m1"])
	nodes[0].Leader, nodes[1].Leader = false, true
	f.nodes = map[string][]agents.SwarmNode{"m1": nodes, "m2": nodes}
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	s.setReader(swarmID, "")
	if got, err := readNow(t, s); err != nil || got != "m2" {
		t.Fatalf("read host %q, %v", got, err)
	}
}

// Unverified hosts never take over: an origin whose agents can't prove
// keeps serving reads alone, and an agent claiming a manager node it does
// not hold is never read through.
func TestUnverifiedNeverTakesOver(t *testing.T) {
	t.Run("old agents", func(t *testing.T) {
		f := lab()
		s := newService(t, f)
		if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
			t.Fatal(err)
		}
		if got, err := readNow(t, s); err != nil || got != "m1" {
			t.Fatalf("read host %q, %v", got, err)
		}
		c, _ := s.Get(context.Background(), swarmID)
		if c.Status.Failover || c.Status.VerifiedManagers != 0 {
			t.Fatalf("status %+v", c.Status)
		}
		setOffline(f, "m1", true)
		if got, err := readNow(t, s); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("failed over to unverified %q (%v)", got, err)
		}
	})
	t.Run("fake manager", func(t *testing.T) {
		f, ca := provedLab(t)
		nodes := append(slices.Clone(f.nodes["m1"]), agents.SwarmNode{ID: "n5", Role: RoleManager})
		f.nodes = map[string][]agents.SwarmNode{"m1": nodes, "m2": nodes}
		f.infos["fake"] = swarmInfo(swarmID, "n5", RoleManager)
		f.provers["fake"] = ca.issue(t, "n4", swarmID, RoleWorker, time.Now().Add(90*24*time.Hour)).prover(t)
		s := newService(t, f)
		s.SetServerID("server-1")
		if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
			t.Fatal(err)
		}
		c, _ := s.Get(context.Background(), swarmID)
		if !c.Status.Failover || c.Status.VerifiedManagers != 2 {
			t.Fatalf("status %+v", c.Status)
		}
		setOffline(f, "m1", true)
		setOffline(f, "m2", true)
		if got, err := readNow(t, s); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("read through %q (%v)", got, err)
		}
	})
}

// While a changed root CA waits, reads stay on the origin.
func TestNoFailoverWhileRootPending(t *testing.T) {
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.roots = map[string]string{"m1": newSwarmCA(t).pem, "m2": f.roots["m2"]}
	f.mu.Unlock()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(context.Background(), swarmID)
	if !c.Trust.Pending || c.Status.Failover {
		t.Fatalf("trust %+v status %+v", c.Trust, c.Status)
	}
	if got := s.readers(c, nil); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("readers %v", got)
	}
}

// A manager that took over reads can't unlink or demote anyone while the
// origin is away, and the origin's list rules again once it answers.
func TestReaderListOnlyAdds(t *testing.T) {
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	setOffline(f, "m1", true)
	forged := []agents.SwarmNode{{ID: "n1", Role: RoleManager}, {ID: "n2", Role: RoleManager, Leader: true}}
	f.mu.Lock()
	f.nodes["m2"] = forged
	f.mu.Unlock()
	if got, err := readNow(t, s); err != nil || got != "m2" {
		t.Fatalf("read host %q, %v", got, err)
	}
	c, _ := s.Get(context.Background(), swarmID)
	if want := map[string]string{"m1": "n1/manager", "m2": "n2/manager", "w1": "n3/worker"}; !equal(roles(c), want) {
		t.Fatalf("members %v", roles(c))
	}
	setOffline(f, "m1", false)
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(context.Background(), swarmID)
	if c.Status.ReadHost != "m2" {
		t.Fatalf("reader %q, want m2 (sticky)", c.Status.ReadHost)
	}
	if roles(c)["w1"] != "n3/worker" {
		t.Fatalf("origin's list not used: %v", roles(c))
	}
}

// An origin whose proof was refused is no fallback once other managers
// are verified.
func TestRefusedOriginNoFallback(t *testing.T) {
	f, ca := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	f.provers["m1"] = ca.issue(t, "n9", swarmID, RoleManager, time.Now().Add(90*24*time.Hour)).prover(t)
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(context.Background(), swarmID)
	if c.Trust.OriginVerified || !slices.Equal(s.readers(c, nil), []string{"m2"}) {
		t.Fatalf("origin verified %v, readers %v", c.Trust.OriginVerified, s.readers(c, nil))
	}
	setOffline(f, "m2", true)
	if got, err := readNow(t, s); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("read through %q (%v)", got, err)
	}
}

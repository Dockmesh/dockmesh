package clusters

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
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

// A refused origin's list can't demote the verified managers and so win
// reads back.
func TestRefusedOriginCantDemote(t *testing.T) {
	f, ca := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	f.provers["m1"] = ca.issue(t, "n9", swarmID, RoleManager, time.Now().Add(90*24*time.Hour)).prover(t)
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	demoted := []agents.SwarmNode{{ID: "n1", Role: RoleManager, Leader: true}, {ID: "n2", Role: RoleWorker}, {ID: "n3", Role: RoleWorker}}
	f.mu.Lock()
	f.nodes["m1"] = demoted
	f.mu.Unlock()
	for range 2 {
		if got, err := readNow(t, s); err != nil || got != "m2" {
			t.Fatalf("read host %q, %v", got, err)
		}
	}
	c, _ := s.Get(context.Background(), swarmID)
	if roles(c)["m2"] != "n2/manager" {
		t.Fatalf("members %v", roles(c))
	}
}

// threeManagers is provedLab with a third manager m3 (node n5).
func threeManagers(t *testing.T) (*fakeHosts, *swarmCA) {
	f, ca := provedLab(t)
	nodes := append(slices.Clone(f.nodes["m1"]), agents.SwarmNode{ID: "n5", Role: RoleManager})
	f.nodes = map[string][]agents.SwarmNode{"m1": nodes, "m2": nodes, "m3": nodes}
	f.infos["m3"] = swarmInfo(swarmID, "n5", RoleManager)
	f.roots["m3"] = ca.pem
	f.provers["m3"] = ca.issue(t, "n5", swarmID, RoleManager, time.Now().Add(90*24*time.Hour)).prover(t)
	return f, ca
}

type rotationAudit struct{ agreedBy [][]string }

func (a *rotationAudit) Write(_ context.Context, _, action, _ string, details any) {
	d, _ := details.(map[string]any)
	if action == audit.ActionClusterTrustRoot && d["change"] == "rotated" {
		got, _ := d["agreed_by"].([]string)
		a.agreedBy = append(a.agreedBy, got)
	}
}

// A rotation is accepted through the verified managers while the origin is
// away, still only when two of them agree, and none of them serves reads
// while the root is pending.
func TestRotationWithoutOrigin(t *testing.T) {
	ctx := context.Background()
	later := time.Now().Add(90 * 24 * time.Hour)
	cases := []struct {
		name string
		// origin is "down" (agent offline) or "refused" (its proof fails).
		origin   string
		m3Proves bool
		// reporting are the managers whose node list reports the new root.
		reporting []string
		// m2Lies: m2 reports another root than the pending one.
		m2Lies   string
		accepted bool
	}{
		{"origin down, m2 and m3 agree", "down", true, []string{"m2", "m3"}, "", true},
		{"origin refused, m2 and m3 agree", "refused", true, []string{"m2", "m3"}, "", true},
		{"origin down, only m2 reports it", "down", true, []string{"m2"}, "", false},
		{"origin down, m3 unverified", "down", false, []string{"m2", "m3"}, "", false},
		{"origin down, m2 reports the old root", "down", true, []string{"m3"}, "old", false},
		{"origin down, m2 reports a third root", "down", true, []string{"m3"}, "third", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ca := threeManagers(t)
			if !tc.m3Proves {
				delete(f.provers, "m3")
			}
			if tc.origin == "refused" {
				f.provers["m1"] = ca.issue(t, "n9", swarmID, RoleManager, later).prover(t)
			}
			s := newService(t, f)
			s.SetServerID("server-1")
			rec := &rotationAudit{}
			s.SetAudit(rec)
			if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
				t.Fatal(err)
			}
			// The root changes on the host reads go through first, so it is
			// pending before the origin goes away.
			next := newSwarmCA(t)
			first := "m1"
			if tc.origin == "refused" {
				first = "m2"
			}
			f.mu.Lock()
			f.roots[first] = next.pem
			f.mu.Unlock()
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if c, _ := s.Get(ctx, swarmID); !c.Trust.Pending {
				t.Fatalf("not pending: %+v", c.Trust)
			}
			if tc.origin == "down" {
				setOffline(f, "m1", true)
			}
			w1Proofs := f.provedCount("w1")
			f.mu.Lock()
			for _, h := range tc.reporting {
				f.roots[h] = next.pem
			}
			switch tc.m2Lies {
			case "old":
				f.roots["m2"] = ca.pem
			case "third":
				f.roots["m2"] = newSwarmCA(t).pem
			}
			f.provers["w1"] = next.issue(t, "n3", swarmID, RoleWorker, later).prover(t)
			for h, n := range map[string]string{"m1": "n1", "m2": "n2", "m3": "n5"} {
				if f.provers[h] != nil && (tc.origin != "refused" || h != "m1") {
					f.provers[h] = next.issue(t, n, swarmID, RoleManager, later).prover(t)
				}
			}
			f.mu.Unlock()
			var before Trust
			for i := range 2 {
				if err := s.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
				c, _ := s.Get(ctx, swarmID)
				if i == 1 && c.Trust.Pending && !reflect.DeepEqual(c.Trust, before) {
					t.Fatalf("second reconcile changed the trust state: %+v, was %+v", c.Trust, before)
				}
				before = c.Trust
			}
			if c, _ := s.Get(ctx, swarmID); c.Trust.Pending {
				if c.trustPending != canonicalRoot(next.pem) {
					t.Fatal("pending root replaced")
				}
				if got, err := s.ReadHost(ctx, swarmID); !errors.Is(err, ErrUnreadable) {
					t.Fatalf("read through %q while the root is pending (%v)", got, err)
				}
				for _, h := range s.readers(c, nil) {
					if h != c.OriginHost {
						t.Fatalf("reader %s while the root is pending", h)
					}
				}
			}
			c, _ := s.Get(ctx, swarmID)
			if got := !c.Trust.Pending && c.trustRoot == canonicalRoot(next.pem); got != tc.accepted {
				t.Fatalf("accepted %v, want %v: %+v", got, tc.accepted, c.Trust)
			}
			if !tc.accepted {
				if !c.Trust.Pending || c.trustPending != canonicalRoot(next.pem) {
					t.Fatalf("pending root lost: %+v", c.Trust)
				}
				if len(rec.agreedBy) != 0 {
					t.Fatalf("rotation audited: %v", rec.agreedBy)
				}
				if c.trustRoot != canonicalRoot(ca.pem) {
					t.Fatal("pinned root changed")
				}
				return
			}
			if len(rec.agreedBy) != 1 || !slices.Equal(rec.agreedBy[0], []string{"m2", "m3"}) {
				t.Fatalf("agreed by %v", rec.agreedBy)
			}
			if f.provedCount("w1") == w1Proofs {
				t.Fatal("w1 not proved again against the new root")
			}
			if got := proofs(c); got["m2"] != ProofVerified || got["m3"] != ProofVerified || got["w1"] != ProofVerified {
				t.Fatalf("after rotation %v", got)
			}
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadHost(ctx, swarmID); err != nil || (got != "m2" && got != "m3") {
				t.Fatalf("read host after rotation %q, %v", got, err)
			}
		})
	}
}

// A single-manager Swarm still waits for an admin when its manager is
// away: there is no second manager to agree.
func TestSingleManagerRotationNeedsAdminWithoutOrigin(t *testing.T) {
	ctx := context.Background()
	f, _ := provedLab(t)
	f.nodes = map[string][]agents.SwarmNode{"m1": {{ID: "n1", Role: RoleManager, Leader: true}, {ID: "n3", Role: RoleWorker}}}
	delete(f.infos, "m2")
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	next := newSwarmCA(t)
	f.mu.Lock()
	f.roots = map[string]string{"m1": next.pem}
	f.mu.Unlock()
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	setOffline(f, "m1", true)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, swarmID)
	if !c.Trust.Pending || !c.Trust.Confirmable || c.trustRoot == canonicalRoot(next.pem) {
		t.Fatalf("trust %+v", c.Trust)
	}
}

// Writes go only through verified managers, in read order; an unverified
// origin never takes one.
func TestWriteHosts(t *testing.T) {
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.WriteHosts(context.Background(), swarmID)
	if err != nil || !reflect.DeepEqual(got, []string{"m1", "m2"}) {
		t.Fatalf("write hosts %v, %v", got, err)
	}
	setOffline(f, "m1", true)
	if got, err = s.WriteHosts(context.Background(), swarmID); err != nil || !reflect.DeepEqual(got, []string{"m2"}) {
		t.Fatalf("m1 offline: %v, %v", got, err)
	}
	setOffline(f, "m2", true)
	if _, err = s.WriteHosts(context.Background(), swarmID); !errors.Is(err, ErrNoWriter) {
		t.Fatalf("all offline: %v", err)
	}

	plain := newService(t, lab())
	if _, err := plain.Create(context.Background(), "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.ReadHost(context.Background(), swarmID); err != nil {
		t.Fatalf("unverified origin still reads: %v", err)
	}
	if _, err := plain.WriteHosts(context.Background(), swarmID); !errors.Is(err, ErrNoWriter) {
		t.Fatalf("unverified origin: %v, want no writer", err)
	}
	if _, err := plain.WriteHosts(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown cluster: %v", err)
	}
}

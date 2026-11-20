package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// fleet is a set of loopback agents that record upgrade frames. An agent
// "comes back" by attaching a fresh session that reports the server's
// version.
type fleet struct {
	t   *testing.T
	svc *Service
	c   *UpgradeController

	mu         sync.Mutex
	dispatched []string
	infos      map[string]*DaemonInfo
}

func managerOf(cluster string) *DaemonInfo {
	return &DaemonInfo{Swarm: &SwarmState{NodeID: "n", NodeState: "active", Role: "manager", ClusterID: cluster}}
}

func workerInfo() *DaemonInfo {
	return &DaemonInfo{Swarm: &SwarmState{NodeID: "n", NodeState: "active", Role: "worker"}}
}

func newFleet(t *testing.T, hold time.Duration) *fleet {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOCKMESH_BINARY_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "dockmesh-agent-linux-amd64"), []byte("agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc, _ := newTestService(t)
	svc.SetNotices(notices.NewService(svc.db))
	f := &fleet{t: t, svc: svc, infos: map[string]*DaemonInfo{}}
	f.c = NewUpgradeController(svc, mapSettings{})
	f.c.managerHoldMax = hold
	f.c.pollEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.c.ctx = ctx
	f.c.SetSwarmState(func(_ context.Context, id string) (*DaemonInfo, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if i, ok := f.infos[id]; ok {
			return i, nil
		}
		return nil, errors.New("unknown")
	})
	return f
}

// connect attaches a session for id reporting ver.
func (f *fleet) connect(id, ver string, info *DaemonInfo) *ConnectedAgent {
	f.mu.Lock()
	f.infos[id] = info
	f.mu.Unlock()
	ag, stop := NewLoopback(id, HelloPayload{ProtocolVersion: ProtocolVersion, Version: ver, Arch: "amd64", Daemon: info},
		func(fr Frame, reply func(Frame)) {
			if fr.Type == FrameReqAgentUpgrade {
				f.mu.Lock()
				f.dispatched = append(f.dispatched, id)
				f.mu.Unlock()
			}
			reply(Frame{Type: FrameRes, ID: fr.ID, Payload: []byte(`{"ok":true}`)})
		})
	f.t.Cleanup(stop)
	f.t.Cleanup(f.svc.AttachLoopback(ag))
	return ag
}

func (f *fleet) comeBack(id string) {
	f.mu.Lock()
	info := f.infos[id]
	f.mu.Unlock()
	f.connect(id, version.Version, info)
}

func (f *fleet) offline(id string) {
	f.svc.mu.Lock()
	delete(f.svc.connected, id)
	f.svc.mu.Unlock()
}

func (f *fleet) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.dispatched)
}

// waitSent waits until n upgrade frames went out.
func (f *fleet) waitSent(n int) []string {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := f.sent(); len(s) >= n {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("only %v dispatched, want %d", f.sent(), n)
	return nil
}

// stays checks that nothing more than n frames go out for a while.
func (f *fleet) stays(n int) {
	f.t.Helper()
	time.Sleep(60 * time.Millisecond)
	if s := f.sent(); len(s) != n {
		f.t.Fatalf("dispatched %v, want %d", s, n)
	}
}

// Managers of one Swarm go one after another, each after the previous
// came back with the new version; a worker, a host outside the Swarm and a
// manager of another Swarm go at once. The same holds for the button
// (manual) and the automatic modes.
func TestManagersUpdateOneAtATime(t *testing.T) {
	for _, manual := range []bool{true, false} {
		name := "auto"
		if manual {
			name = "button"
		}
		t.Run(name, func(t *testing.T) {
			f := newFleet(t, time.Minute)
			agents := map[string]*ConnectedAgent{
				"m1":   f.connect("m1", "old", managerOf("c1")),
				"m2":   f.connect("m2", "old", managerOf("c1")),
				"m3":   f.connect("m3", "old", managerOf("c1")),
				"w1":   f.connect("w1", "old", workerInfo()),
				"solo": f.connect("solo", "old", nil),
				"x1":   f.connect("x1", "old", managerOf("c2")),
			}
			ctx := context.Background()
			results := map[string]error{}
			for _, id := range []string{"m1", "m2", "m3", "w1", "solo", "x1"} {
				_, err := f.c.upgradeOne(ctx, agents[id], manual)
				results[id] = err
			}
			for id, want := range map[string]error{"m1": nil, "m2": ErrUpgradeQueued, "m3": ErrUpgradeQueued, "w1": nil, "solo": nil, "x1": nil} {
				if !errors.Is(results[id], want) {
					t.Fatalf("%s: %v, want %v", id, results[id], want)
				}
			}
			first := f.waitSent(4)
			f.stays(4)
			if slices.Contains(first, "m2") || slices.Contains(first, "m3") {
				t.Fatalf("a second manager went before the first was back: %v", first)
			}
			// Asking again while queued doesn't queue twice.
			if _, err := f.c.upgradeOne(ctx, agents["m2"], manual); !errors.Is(err, ErrUpgradeQueued) {
				t.Fatalf("again: %v", err)
			}

			f.comeBack("m1")
			second := f.waitSent(5)
			f.stays(5)
			next := second[4]
			if next != "m2" && next != "m3" {
				t.Fatalf("next %v", second)
			}
			f.comeBack(next)
			third := f.waitSent(6)
			f.stays(6)
			if third[5] == next || (third[5] != "m2" && third[5] != "m3") {
				t.Fatalf("last %v", third)
			}
		})
	}
}

// A manager that is not back within the hold time lets the next one go
// on, with a notice for admins; coming back late sends nothing more.
func TestManagerTimeoutGoesOnWithNotice(t *testing.T) {
	f := newFleet(t, 50*time.Millisecond)
	m1 := f.connect("m1", "old", managerOf("c1"))
	m2 := f.connect("m2", "old", managerOf("c1"))
	ctx := context.Background()
	if _, err := f.c.upgradeOne(ctx, m1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.upgradeOne(ctx, m2, true); !errors.Is(err, ErrUpgradeQueued) {
		t.Fatal(err)
	}
	f.waitSent(1)
	start := time.Now()
	got := f.waitSent(2)
	if got[1] != "m2" || time.Since(start) < 30*time.Millisecond {
		t.Fatalf("dispatched %v after %v", got, time.Since(start))
	}
	var n int
	if err := f.svc.db.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = ?`, managerTimeoutNoticePrefix+"m1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d timeout notices", n)
	}
	f.comeBack("m1")
	f.stays(2)
}

// A manager that is offline is never waited for, and one that goes
// offline while queued is skipped when its turn comes.
func TestOfflineManagerIsSkipped(t *testing.T) {
	f := newFleet(t, time.Minute)
	f.connect("m1", "old", managerOf("c1"))
	f.connect("m2", "old", managerOf("c1"))
	f.connect("m3", "old", managerOf("c1"))
	ctx := context.Background()

	// m2 drops off before the update starts; the evaluator only sees the
	// connected agents.
	f.offline("m2")
	f.c.settings = mapSettings{UpgradeModeKey: UpgradeModeAuto}
	f.c.Evaluate(ctx)
	f.waitSent(1)
	f.stays(1)
	first := f.sent()[0]
	if first != "m1" && first != "m3" {
		t.Fatalf("dispatched %v", f.sent())
	}
	other := "m3"
	if first == "m3" {
		other = "m1"
	}

	// The queued one goes offline; when the first is back nobody is left.
	f.offline(other)
	f.comeBack(first)
	f.stays(1)
	if slices.Contains(f.sent(), "m2") {
		t.Fatal("offline manager got an update")
	}

	// A new session of the skipped manager is updated right away.
	f.connect(other, "old", managerOf("c1"))
	if _, err := f.c.UpgradeOne(ctx, other); err != nil {
		t.Fatal(err)
	}
	if got := f.waitSent(2); got[1] != other {
		t.Fatalf("dispatched %v", got)
	}
}

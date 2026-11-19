package migration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// insertInterrupted writes the record a migration leaves when the server
// stops while it runs phase: run stores the phase as status and phase.
func (e *migrationEnv) insertInterrupted(t *testing.T, phase string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO migrations (id, stack_name, source_host_id, target_host_id, status, phase, initiated_by)
		VALUES ('m1', 'web', 'agent0', 'agent1', ?, ?, 'u1')`, phase, phase); err != nil {
		t.Fatal(err)
	}
}

func (e *migrationEnv) record(t *testing.T) *Migration {
	t.Helper()
	m, err := e.svc.Store().Get(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *migrationEnv) waitStatus(t *testing.T, want string) *Migration {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := e.record(t); m.Status == want {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	m := e.record(t)
	t.Fatalf("status = %s (%s), want %s", m.Status, m.ErrorMessage, want)
	return nil
}

// A restart before the source was stopped leaves the stack where it runs;
// from the stop on, the stack is restored on the source. Starting twice
// changes nothing more.
func TestRestartDuringMigration(t *testing.T) {
	tests := []struct {
		phase         string
		afterStart    string
		final         string
		sourceDeploys int
		targetStops   int
	}{
		{StatusPreparing, StatusFailed, StatusFailed, 0, 0},
		{StatusPreDump, StatusFailed, StatusFailed, 0, 0},
		{StatusStopping, StatusRollingBack, StatusRolledBack, 1, 1},
		{StatusSyncing, StatusRollingBack, StatusRolledBack, 1, 1},
		{StatusStarting, StatusRollingBack, StatusRolledBack, 1, 1},
		{StatusHealthCheck, StatusRollingBack, StatusRolledBack, 1, 1},
		{StatusCommitting, StatusRollingBack, StatusRolledBack, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			e := newMigrationEnv(t)
			e.source.deployed = false
			e.insertInterrupted(t, tt.phase)
			for start := 1; start <= 2; start++ {
				if err := e.svc.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				if m := e.record(t); m.Status != tt.afterStart || m.Phase != tt.phase {
					t.Fatalf("start %d: %s/%s, want %s/%s", start, m.Status, m.Phase, tt.afterStart, tt.phase)
				}
			}
			e.svc.Recover(context.Background())
			m := e.waitStatus(t, tt.final)
			deploys, stops := e.source.deployCount(), e.target.stopCount()
			if deploys != tt.sourceDeploys || stops < tt.targetStops {
				t.Fatalf("source deploys %d, target stops %d; want %d, at least %d (%s)", deploys, stops, tt.sourceDeploys, tt.targetStops, m.ErrorMessage)
			}
		})
	}
}

// Recovery waits for an offline source instead of giving up, and the
// migration stays rolling_back meanwhile.
func TestRecoveryWaitsForOfflineSource(t *testing.T) {
	shortHostWait(t)
	e := newMigrationEnv(t)
	e.insertInterrupted(t, StatusSyncing)
	e.source.offline.Store(true)
	if err := e.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.svc.Recover(context.Background())
	time.Sleep(100 * time.Millisecond)
	if m := e.record(t); m.Status != StatusRollingBack {
		t.Fatalf("source offline: status %s (%s)", m.Status, m.ErrorMessage)
	}
	e.source.offline.Store(false)
	e.waitStatus(t, StatusRolledBack)
}

func (f *fakeHost) deployCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deploys)
}

func (f *fakeHost) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

func shortHostWait(t *testing.T) {
	t.Helper()
	saved := hostWait
	hostWait = 10 * time.Millisecond
	t.Cleanup(func() { hostWait = saved })
}

// A restart while a manual rollback of a completed migration waits stops
// the stack on the target before the source runs it again.
func TestRestartDuringManualRollback(t *testing.T) {
	shortHostWait(t)
	e := newMigrationEnv(t)
	m := e.migrate(t)
	if m.Status != StatusCompleted {
		t.Fatalf("migration %s (%s)", m.Status, m.ErrorMessage)
	}
	e.source.offline.Store(true)
	if err := e.svc.Rollback(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rollback(context.Background(), m.ID); err == nil {
		t.Fatal("a second rollback of the same migration started")
	}
	restarted := NewService(e.db, fakeHosts{"agent0": e.source, "agent1": e.target}, e.mgr, e.svc.deployments)
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopsBefore := e.target.stopCount()
	restarted.Recover(context.Background())
	e.source.offline.Store(false)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.svc.Store().Get(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == StatusRolledBack && e.target.stopCount() >= stopsBefore+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("target stops %d (before %d), want both rollbacks to stop the target", e.target.stopCount(), stopsBefore)
}

func TestRecoveryEnds(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *migrationEnv)
		want  string
	}{
		{"source host deleted", func(e *migrationEnv) { e.source.gone.Store(true) }, "unknown host"},
		{"stack deployed elsewhere", func(e *migrationEnv) {
			if err := e.svc.deployments.Set(context.Background(), "web", "agent9", "deployed"); err != nil {
				t.Fatal(err)
			}
		}, "deployed on agent9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shortHostWait(t)
			e := newMigrationEnv(t)
			e.insertInterrupted(t, StatusSyncing)
			tt.setup(e)
			if err := e.svc.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			e.svc.Recover(context.Background())
			m := e.waitStatus(t, StatusFailed)
			if !strings.Contains(m.ErrorMessage, tt.want) {
				t.Fatalf("error = %q, want %q", m.ErrorMessage, tt.want)
			}
			if e.source.deployCount() != 0 {
				t.Fatal("source restarted")
			}
		})
	}
}

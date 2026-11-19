package migration

import (
	"context"
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
		{StatusStopping, StatusRollingBack, StatusRolledBack, 1, 0},
		{StatusSyncing, StatusRollingBack, StatusRolledBack, 1, 0},
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
			e.source.mu.Lock()
			deploys := len(e.source.deploys)
			e.source.mu.Unlock()
			if deploys != tt.sourceDeploys || e.target.stops != tt.targetStops {
				t.Fatalf("source deploys %d, target stops %d; want %d, %d (%s)", deploys, e.target.stops, tt.sourceDeploys, tt.targetStops, m.ErrorMessage)
			}
		})
	}
}

// Recovery waits for an offline source instead of giving up, and the
// migration stays rolling_back meanwhile.
func TestRecoveryWaitsForOfflineSource(t *testing.T) {
	saved := hostWait
	hostWait = 10 * time.Millisecond
	t.Cleanup(func() { hostWait = saved })
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

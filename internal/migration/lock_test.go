package migration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/stacklock"
)

func newLockedEnv(t *testing.T) (*migrationEnv, *stacklock.Locks) {
	t.Helper()
	e := newMigrationEnv(t)
	l := stacklock.New()
	e.svc.SetLocks(l)
	return e, l
}

// busyWith returns the operation holding key, or "" when it is free.
func busyWith(t *testing.T, l *stacklock.Locks, key stacklock.Key) string {
	t.Helper()
	release, err := l.TryLock(key, "probe")
	if err == nil {
		release()
		return ""
	}
	var busy *stacklock.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("TryLock: %v", err)
	}
	return busy.Operation
}

var (
	sourceKey = stacklock.HostKey("agent0", "web")
	targetKey = stacklock.HostKey("agent1", "web")
)

func TestInitiateRefusesBusyStack(t *testing.T) {
	for _, key := range []stacklock.Key{sourceKey, targetKey} {
		t.Run(key.Target.ID, func(t *testing.T) {
			e, l := newLockedEnv(t)
			release, err := l.TryLock(key, stacklock.OpDeploy)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.svc.Initiate(context.Background(), "web", "agent1", "u1", "", nil)
			var busy *stacklock.BusyError
			if !errors.As(err, &busy) || busy.Operation != stacklock.OpDeploy {
				t.Fatalf("initiate during a deploy: %v", err)
			}
			if active, _ := e.svc.Store().HasActive(context.Background(), "web"); active {
				t.Fatal("a refused migration was recorded")
			}
			other := sourceKey
			if key == sourceKey {
				other = targetKey
			}
			if op := busyWith(t, l, other); op != "" {
				t.Fatalf("refused migration kept %v locked by %s", other, op)
			}
			release()
			if m := e.migrate(t); m.Status != StatusCompleted {
				t.Fatalf("migration after the deploy: %s (%s)", m.Status, m.ErrorMessage)
			}
		})
	}
}

// The run holds the stack on both hosts while it changes them and gives
// both back once it ends.
func TestMigrationHoldsStackLocks(t *testing.T) {
	e, l := newLockedEnv(t)
	seen := make(chan [2]string, 1)
	e.target.onDeploy = func() {
		seen <- [2]string{busyWith(t, l, sourceKey), busyWith(t, l, targetKey)}
	}
	if m := e.migrate(t); m.Status != StatusCompleted {
		t.Fatalf("migration: %s (%s)", m.Status, m.ErrorMessage)
	}
	if got := <-seen; got != [2]string{stacklock.OpMigration, stacklock.OpMigration} {
		t.Fatalf("during the target deploy source/target held by %q", got)
	}
	for _, k := range []stacklock.Key{sourceKey, targetKey} {
		if op := busyWith(t, l, k); op != "" {
			t.Fatalf("%v still held by %s after the migration", k, op)
		}
	}
}

// A rollback waiting for its offline target holds no lock; once the
// target is back it waits for a running deploy before it writes.
func TestRollbackLocksOnlyItsWrites(t *testing.T) {
	shortHostWait(t)
	e, l := newLockedEnv(t)
	m := e.migrate(t)
	if m.Status != StatusCompleted {
		t.Fatalf("migration: %s (%s)", m.Status, m.ErrorMessage)
	}
	e.target.offline.Store(true)
	if err := e.svc.Rollback(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, k := range []stacklock.Key{sourceKey, targetKey} {
		if op := busyWith(t, l, k); op != "" {
			t.Fatalf("waiting for the target, %v is held by %s", k, op)
		}
	}
	release, err := l.TryLock(sourceKey, stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	stops := e.target.stopCount()
	e.target.offline.Store(false)
	time.Sleep(100 * time.Millisecond)
	if got, _ := e.svc.Store().Get(context.Background(), m.ID); got.Status != StatusRollingBack {
		t.Fatalf("rollback went on while a deploy held the stack: %s", got.Status)
	}
	if e.target.stopCount() != stops || e.source.deployCount() != 0 {
		t.Fatal("rollback wrote while a deploy held the stack")
	}
	release()
	got := e.wait(t, m.ID)
	if got.Status != StatusRolledBack {
		t.Fatalf("rollback: %s (%s)", got.Status, got.ErrorMessage)
	}
	for _, k := range []stacklock.Key{sourceKey, targetKey} {
		if op := busyWith(t, l, k); op != "" {
			t.Fatalf("%v still held by %s after the rollback", k, op)
		}
	}
}

func TestMigrationKeysOrderedByHost(t *testing.T) {
	tests := []struct{ source, target string }{
		{"agent0", "agent1"},
		{"agent1", "agent0"},
	}
	for _, tt := range tests {
		keys := migrationKeys(&Migration{StackName: "web", SourceHostID: tt.source, TargetHostID: tt.target})
		if keys[0] != sourceKey || keys[1] != targetKey {
			t.Errorf("%s→%s: keys %v, want agent0 first", tt.source, tt.target, keys)
		}
	}
}

// With a later migration of the stack running back to the source, the
// earlier one can neither be rolled back nor finalised.
func TestCompletedMigrationRefusedWhileAnotherRuns(t *testing.T) {
	e, l := newLockedEnv(t)
	m1 := e.migrate(t)
	if m1.Status != StatusCompleted {
		t.Fatalf("migration: %s (%s)", m1.Status, m1.ErrorMessage)
	}
	if _, err := e.db.Exec(`INSERT INTO migrations (id, stack_name, source_host_id, target_host_id, status, phase, initiated_by)
		VALUES ('m2', 'web', 'agent1', 'agent0', ?, ?, 'u1')`, StatusSyncing, StatusSyncing); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.svc.Rollback(ctx, m1.ID); !errors.Is(err, ErrMigrationActive) {
		t.Fatalf("rollback of m1: %v", err)
	}
	if err := e.svc.PurgeSource(ctx, m1.ID); !errors.Is(err, ErrMigrationActive) {
		t.Fatalf("purge of m1: %v", err)
	}
	release, err := l.TryLock(sourceKey, stacklock.OpMigration)
	if err != nil {
		t.Fatal(err)
	}
	var busy *stacklock.BusyError
	if err := e.svc.PurgeSource(ctx, m1.ID); !errors.As(err, &busy) {
		t.Fatalf("purge with the source held: %v", err)
	}
	release()
	if got, _ := e.svc.Store().Get(ctx, m1.ID); got.Status != StatusCompleted || got.Progress.Purged {
		t.Fatalf("m1 changed: %s purged=%v", got.Status, got.Progress.Purged)
	}
	if e.target.stopCount() != 0 || e.source.stopCount() != 1 {
		t.Fatalf("stops: target %d source %d", e.target.stopCount(), e.source.stopCount())
	}
	if _, err := e.svc.Initiate(ctx, "web", "agent0", "u1", "", nil); !errors.Is(err, ErrMigrationActive) {
		t.Fatalf("third migration: %v", err)
	}
}

// A deploy that moved the stack while the rollback waited for its target
// is left alone: the target is not stopped.
func TestRollbackRechecksAfterWaiting(t *testing.T) {
	shortHostWait(t)
	e, _ := newLockedEnv(t)
	m := e.migrate(t)
	if m.Status != StatusCompleted {
		t.Fatalf("migration: %s (%s)", m.Status, m.ErrorMessage)
	}
	e.target.offline.Store(true)
	if err := e.svc.Rollback(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := e.svc.deployments.Set(context.Background(), "web", "agent2", "deployed"); err != nil {
		t.Fatal(err)
	}
	stops := e.target.stopCount()
	e.target.offline.Store(false)
	got := e.wait(t, m.ID)
	if got.Status != StatusFailed || e.target.stopCount() != stops || e.source.deployCount() != 0 {
		t.Fatalf("status %s (%s), target stops %d→%d, source deploys %d", got.Status, got.ErrorMessage, stops, e.target.stopCount(), e.source.deployCount())
	}
}

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

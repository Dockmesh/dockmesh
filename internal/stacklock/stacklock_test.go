package stacklock

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTryLock(t *testing.T) {
	l := New()
	web := HostKey("local", "web")
	release, err := l.TryLock(web, OpDeploy)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		key     Key
		wantErr bool
	}{
		{"same stack refused", web, true},
		{"empty host id is local", HostKey("", "web"), true},
		{"other stack", HostKey("local", "db"), false},
		{"same stack on another host", HostKey("agent-1", "web"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, err := l.TryLock(tt.key, OpImageUpdate)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("TryLock: %v", err)
				}
				rel()
				return
			}
			var busy *BusyError
			if !errors.As(err, &busy) || !errors.Is(err, ErrBusy) {
				t.Fatalf("err = %v, want a BusyError", err)
			}
			if busy.Operation != OpDeploy || busy.Stack != "web" || busy.Since.IsZero() {
				t.Fatalf("busy = %+v", busy)
			}
		})
	}

	release()
	release()
	rel, err := l.TryLock(web, OpImageUpdate)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	rel()
}

func TestNilLocks(t *testing.T) {
	var l *Locks
	rel, err := l.TryLock(HostKey("", "web"), OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if _, err := l.Lock(context.Background(), HostKey("", "web"), OpDeploy); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Maintenance(context.Background(), []Target{Host("")}, OpMaintenance, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentOneWins(t *testing.T) {
	l := New()
	key := HostKey("local", "web")
	var wins atomic.Int32
	var wg, tried sync.WaitGroup
	start := make(chan struct{})
	hold := make(chan struct{})
	for _, op := range []string{OpDeploy, OpImageUpdate, OpScale, OpRolling} {
		wg.Add(1)
		tried.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rel, err := l.TryLock(key, op)
			tried.Done()
			if err != nil {
				if !errors.Is(err, ErrBusy) {
					t.Errorf("%s: %v", op, err)
				}
				return
			}
			wins.Add(1)
			<-hold
			rel()
		}()
	}
	close(start)
	tried.Wait()
	close(hold)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d operations got the lock, want 1", wins.Load())
	}
}

func TestLockWaits(t *testing.T) {
	l := New()
	key := HostKey("local", "web")
	release, err := l.TryLock(key, OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		rel, err := l.Lock(context.Background(), key, OpGitDeploy)
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("Lock returned while held: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lock did not return after release")
	}
}

func TestLockCancel(t *testing.T) {
	l := New()
	key := HostKey("local", "web")
	release, err := l.TryLock(key, OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := l.Lock(ctx, key, OpGitDeploy)
		got <- err
	}()
	cancel()
	err = <-got
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want busy and cancelled", err)
	}
	release()
	rel, err := l.TryLock(key, OpScale)
	if err != nil {
		t.Fatalf("a cancelled Lock left the stack locked: %v", err)
	}
	rel()
}

func TestReleaseOnError(t *testing.T) {
	l := New()
	key := HostKey("local", "web")
	failing := func() (err error) {
		rel, err := l.TryLock(key, OpDeploy)
		if err != nil {
			return err
		}
		defer rel()
		return errors.New("deploy failed")
	}
	if err := failing(); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("first run: %v", err)
	}
	if err := failing(); errors.Is(err, ErrBusy) {
		t.Fatal("lock still held after a failed operation")
	}
}

func TestMaintenance(t *testing.T) {
	l := New()
	local, agent := Host("local"), Host("agent-1")
	web := HostKey("local", "web")
	release, err := l.TryLock(web, OpDeploy)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	var maintRelease func()
	go func() {
		rel, err := l.Maintenance(context.Background(), []Target{local}, OpMaintenance, 5*time.Second)
		maintRelease = rel
		got <- err
	}()
	waitFor(t, func() bool {
		rel, err := l.TryLock(HostKey("local", "db"), OpDeploy)
		if err == nil {
			rel()
		}
		return errors.Is(err, ErrBusy)
	})
	rel, err := l.TryLock(HostKey("agent-1", "db"), OpDeploy)
	if err != nil {
		t.Fatalf("other host blocked by maintenance: %v", err)
	}
	rel()
	select {
	case err := <-got:
		t.Fatalf("maintenance returned while a deploy runs: %v", err)
	default:
	}

	release()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	var busy *BusyError
	if _, err := l.TryLock(web, OpDeploy); !errors.As(err, &busy) || busy.Operation != OpMaintenance {
		t.Fatalf("deploy during maintenance: %v", err)
	}
	if _, err := l.Maintenance(context.Background(), []Target{local, agent}, OpMaintenance, time.Second); !errors.Is(err, ErrBusy) {
		t.Fatalf("second maintenance: %v, want busy", err)
	}
	maintRelease()
	rel, err = l.TryLock(web, OpDeploy)
	if err != nil {
		t.Fatalf("after maintenance: %v", err)
	}
	rel()
}

func TestMaintenanceTimeout(t *testing.T) {
	l := New()
	release, err := l.TryLock(HostKey("local", "web"), OpRolling)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = l.Maintenance(context.Background(), []Target{Host("local")}, OpMaintenance, 50*time.Millisecond)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want busy after the deadline", err)
	}
	if want := "web (rolling update)"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to name %q", err, want)
	}
	rel, err := l.TryLock(HostKey("local", "db"), OpDeploy)
	if err != nil {
		t.Fatalf("timed-out maintenance still blocks: %v", err)
	}
	rel()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

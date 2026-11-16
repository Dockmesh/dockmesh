package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

func (f *installFixture) waitUnit(phases ...string) UnitRewriteState {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := f.inst.UnitState()
		for _, p := range phases {
			if st.Phase == p {
				return st
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("unit state %+v, want one of %v", st, phases)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// unitUpdater takes the request like root does and answers with res, or
// with nothing when res is nil (root restarted the server).
func (f *installFixture) unitUpdater(res *rootupdate.UnitResult) <-chan rootupdate.Request {
	got := make(chan rootupdate.Request, 1)
	go func() {
		p := filepath.Join(f.dir, rootupdate.RequestFile)
		for range 500 {
			if data, err := os.ReadFile(p); err == nil {
				var r rootupdate.Request
				_ = json.Unmarshal(data, &r)
				_ = os.Remove(p)
				if res != nil {
					b, _ := json.Marshal(res)
					_ = os.WriteFile(filepath.Join(f.dir, rootupdate.UnitResultFile), b, 0o644)
				}
				got <- r
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(got)
	}()
	return got
}

func TestUnitRewriteWritesRequestOnly(t *testing.T) {
	f := newInstallFixture(t)
	got := f.unitUpdater(nil)
	if err := f.inst.StartUnitRewrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := <-got
	if req.Type != rootupdate.RequestUnitRewrite || req.Version != "" {
		t.Fatalf("request %+v, want only the type", req)
	}
	st := f.waitUnit(UnitPhaseRestarting)
	if !st.RestartPending {
		t.Fatalf("state %+v, want restart_pending", st)
	}
	// Until root restarts the server, no stack operation may start.
	if _, err := f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy); !errors.Is(err, stacklock.ErrBusy) {
		t.Fatalf("stack operation allowed while the restart is pending: %v", err)
	}
	if err := f.inst.StartUnitRewrite(context.Background()); !errors.Is(err, ErrUnitRewriteRunning) {
		t.Fatalf("second rewrite: %v", err)
	}
	if err := f.inst.Start(context.Background(), "v0.6.1"); !errors.Is(err, ErrUnitRewriteRunning) {
		t.Fatalf("update while a rewrite is pending: %v", err)
	}
}

func TestUnitRewriteRefusedByRoot(t *testing.T) {
	f := newInstallFixture(t)
	f.unitUpdater(&rootupdate.UnitResult{Status: rootupdate.UnitRefused, Step: "unit", Reason: `writable path "/etc/x" lies in /etc`})
	if err := f.inst.StartUnitRewrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.waitUnit(UnitPhaseFailed)
	if !strings.Contains(st.Error, "/etc/x") || st.RestartPending || st.LastResult == nil || st.LastResult.Status != rootupdate.UnitRefused {
		t.Fatalf("state %+v", st)
	}
	open, _ := f.notices.Open(context.Background())
	if len(open) != 1 || open[0].Key != UnitNoticeKey {
		t.Fatalf("notices %+v, want the unit notice", open)
	}
	// The stacks are free again.
	release, err := f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestUnitRewriteRefusals(t *testing.T) {
	f := newInstallFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, rootupdate.PendingFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.inst.StartUnitRewrite(context.Background()); !errors.Is(err, ErrInstallRunning) {
		t.Fatalf("while an update is pending: %v", err)
	}
	_ = os.Remove(filepath.Join(f.dir, rootupdate.PendingFile))
	f.inst.cfg.UpdaterInstalled = func() bool { return false }
	if err := f.inst.StartUnitRewrite(context.Background()); !errors.Is(err, ErrUpdaterMissing) {
		t.Fatalf("without the updater: %v", err)
	}
	f.inst.cfg.UpdaterInstalled = func() bool { return true }
	f.inst.cfg.InContainer = func() bool { return true }
	if err := f.inst.StartUnitRewrite(context.Background()); !errors.Is(err, ErrInContainer) {
		t.Fatalf("in a container: %v", err)
	}
}

func TestUnitRewriteWaitsForBusyStacks(t *testing.T) {
	f := newInstallFixture(t)
	release, err := f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := f.inst.StartUnitRewrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.waitUnit(UnitPhaseFailed)
	if st.Error != errStacksStillBusy.Error() {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
		t.Fatal("request written while a stack was busy")
	}
}

func TestUnitRewriteNoAnswer(t *testing.T) {
	f := newInstallFixture(t)
	f.inst.cfg.ApplyWait = 100 * time.Millisecond
	if err := f.inst.StartUnitRewrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.waitUnit(UnitPhaseFailed)
	if st.Error != errUpdaterNoAnswer.Error() {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
		t.Fatal("stale request left for the updater")
	}
}

func TestCheckUnitResult(t *testing.T) {
	tests := []struct {
		status string
		notice bool
	}{
		{rootupdate.UnitDone, false},
		{rootupdate.UnitRefused, true},
		{rootupdate.UnitRolledBack, true},
		{rootupdate.UnitFailed, true},
		// Root never got past the restart.
		{rootupdate.UnitRestarting, true},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			f := newInstallFixture(t)
			b, _ := json.Marshal(rootupdate.UnitResult{Status: tt.status, Reason: "why"})
			if err := os.WriteFile(filepath.Join(f.dir, rootupdate.UnitResultFile), b, 0o644); err != nil {
				t.Fatal(err)
			}
			res := f.inst.CheckUnitResult(context.Background())
			if tt.status != rootupdate.UnitRestarting && res.Status != tt.status {
				t.Fatalf("result %+v", res)
			}
			open, _ := f.notices.Open(context.Background())
			if tt.notice != (len(open) == 1) {
				t.Fatalf("notices %+v, want notice=%v", open, tt.notice)
			}
			if _, err := os.Stat(filepath.Join(f.dir, rootupdate.UnitResultFile)); !os.IsNotExist(err) {
				t.Fatal("result file kept")
			}
			// A later successful rewrite clears the notice.
			b, _ = json.Marshal(rootupdate.UnitResult{Status: rootupdate.UnitDone})
			_ = os.WriteFile(filepath.Join(f.dir, rootupdate.UnitResultFile), b, 0o644)
			f.inst.CheckUnitResult(context.Background())
			if open, _ := f.notices.Open(context.Background()); len(open) != 0 {
				t.Fatalf("notices %+v after a successful rewrite", open)
			}
		})
	}
}

func TestAwaitUnitResultWaitsForRestart(t *testing.T) {
	f := newInstallFixture(t)
	old := pendingPoll
	pendingPoll = 5 * time.Millisecond
	defer func() { pendingPoll = old }()
	write := func(status string) {
		b, _ := json.Marshal(rootupdate.UnitResult{Status: status})
		if err := os.WriteFile(filepath.Join(f.dir, rootupdate.UnitResultFile), b, 0o644); err != nil {
			t.Error(err)
		}
	}
	write(rootupdate.UnitRestarting)
	done := make(chan struct{})
	go func() {
		f.inst.AwaitUnitResult(context.Background())
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("returned while root still checks the restart")
	default:
	}
	write(rootupdate.UnitDone)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not return after the final result")
	}
	if st := f.inst.UnitState(); st.LastResult == nil || st.LastResult.Status != rootupdate.UnitDone {
		t.Fatalf("state %+v", st)
	}
}

func TestCheckUnitResultTwoStarts(t *testing.T) {
	f := newInstallFixture(t)
	b, _ := json.Marshal(rootupdate.UnitResult{Status: rootupdate.UnitRolledBack, Reason: "why"})
	if err := os.WriteFile(filepath.Join(f.dir, rootupdate.UnitResultFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	f.inst.AwaitUnitResult(context.Background())
	first, _ := f.notices.Open(context.Background())
	// The next start finds no result and changes nothing.
	again, err := NewInstaller(f.inst.cfg)
	if err != nil {
		t.Fatal(err)
	}
	again.AwaitUnitResult(context.Background())
	second, _ := f.notices.Open(context.Background())
	if len(first) != 1 || len(second) != 1 || first[0].ID != second[0].ID {
		t.Fatalf("notices after the first start %+v, after the second %+v", first, second)
	}
	if res := again.CheckUnitResult(context.Background()); res.Status != "" {
		t.Fatalf("second start read a result %+v", res)
	}
}

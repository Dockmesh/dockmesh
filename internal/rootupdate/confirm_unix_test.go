//go:build unix

package rootupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// cutOffFixture runs an update to v0.6.1 whose service reports healthy
// only while online is true, e.g. an agent that has to reach its server.
func cutOffFixture(t *testing.T, confirmLater bool) (*fixture, *bool, Config) {
	t.Helper()
	f := newFixture(t)
	online := false
	f.svc.healthy = func(v string) bool { return online && f.svc.running && f.svc.version() == v }
	f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
	cfg := f.config()
	cfg.ConfirmRollbackLater = confirmLater
	return f, &online, cfg
}

func (f *fixture) confirmRequest() {
	f.t.Helper()
	b, _ := json.Marshal(Request{Type: RequestConfirmRollback})
	f.write(filepath.Join(f.reqDir, RequestFile), string(b))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (f *fixture) applyWith(cfg Config) Result {
	f.t.Helper()
	res, err := Apply(context.Background(), cfg)
	if err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
	return res
}

// A rollback stays failed without ConfirmRollbackLater, and when it could
// not put everything back.
func TestApplyRollbackStaysFailed(t *testing.T) {
	for _, tt := range []struct {
		name         string
		confirmLater bool
		failAt       string
	}{
		{"without ConfirmRollbackLater", false, ""},
		{"restore error", true, "restore-unit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, _, cfg := cutOffFixture(t, tt.confirmLater)
			if tt.failAt != "" {
				f.svc.failAt = map[string]error{tt.failAt: errors.New("read-only")}
			}
			if res := f.applyWith(cfg); res.Status != StatusFailed {
				t.Fatalf("result %+v, want failed", res)
			}
			if !exists(filepath.Join(f.reqDir, PendingFile)) {
				t.Fatal("pending marker removed after a failed rollback")
			}
		})
	}
}

func TestApplyRollbackUnconfirmedUntilOldVersionAnswers(t *testing.T) {
	tests := []struct {
		name    string
		online  bool
		request bool // the agent's confirm request; false = boot run
		want    Status
	}{
		{"confirm request while still cut off", false, true, StatusUnconfirmed},
		{"boot run while still cut off", false, false, StatusUnconfirmed},
		{"confirm request once back", true, true, StatusRolledBack},
		{"boot run once back", true, false, StatusRolledBack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, online, cfg := cutOffFixture(t, true)
			first := f.applyWith(cfg)
			if first.Status != StatusUnconfirmed || first.Step != StepHealth || !strings.Contains(first.Reason, "v0.6.1 did not report healthy") {
				t.Fatalf("first run %+v, want unconfirmed at the health step", first)
			}
			if got := f.storedResult(); got.Status != StatusUnconfirmed || got.From != "v0.6.0" || got.To != "v0.6.1" {
				t.Fatalf("stored result %+v", got)
			}
			if !exists(filepath.Join(f.reqDir, PendingFile)) || !exists(filepath.Join(f.stateDir, journalName)) {
				t.Fatal("pending marker or journal gone before the rollback is confirmed")
			}
			if got := f.read(filepath.Join(f.binDir, "dockmesh")); got != "bin v0.6.0" || !f.svc.running {
				t.Fatalf("binary %q running %v, want the old version started", got, f.svc.running)
			}

			*online = tt.online
			f.svc.calls = nil
			if tt.request {
				f.confirmRequest()
			}
			second := f.applyWith(cfg)
			if second.Status != tt.want || second.Step != first.Step || second.Reason != first.Reason || second.From != "v0.6.0" || second.To != "v0.6.1" {
				t.Fatalf("second run %+v, want %s with the first run's step and reason", second, tt.want)
			}
			if slices.Contains(f.svc.calls, "stop") || slices.Contains(f.svc.calls, "start") {
				t.Errorf("confirming restarted the service: %v", f.svc.calls)
			}
			if exists(filepath.Join(f.reqDir, RequestFile)) {
				t.Error("confirm request left behind")
			}
			if got := f.storedResult(); got.Status != tt.want {
				t.Errorf("stored result %+v, want %s", got, tt.want)
			}
			if tt.want == StatusUnconfirmed {
				if !exists(filepath.Join(f.reqDir, PendingFile)) {
					t.Error("pending marker removed while unconfirmed")
				}
				return
			}
			f.assertOldRunning(false)

			// Later runs, a stray confirm request included, change nothing.
			for _, request := range []bool{false, true} {
				f.svc.calls = nil
				if request {
					f.confirmRequest()
				}
				if res := f.applyWith(cfg); res.Status != "" {
					t.Fatalf("run after the confirmation: %+v", res)
				}
				if got := f.storedResult(); got.Status != StatusRolledBack {
					t.Errorf("result overwritten: %+v", got)
				}
				f.assertServiceUntouched()
				if exists(filepath.Join(f.reqDir, RequestFile)) {
					t.Error("stray confirm request left behind")
				}
			}

			// The next update goes ahead.
			*online = true
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			if res := f.applyWith(cfg); res.Status != StatusUpdated {
				t.Fatalf("update after the confirmed rollback: %+v", res)
			}
		})
	}
}

// reportClock fakes a health report written at reportedAt.
type reportClock struct {
	*fakeService
	reportedAt time.Time
}

func (r *reportClock) HealthySince(ctx context.Context, v string, since time.Time) bool {
	return !r.reportedAt.Before(since) && r.Healthy(ctx, v)
}

// A health report from before the rollback started the old version does
// not confirm it.
func TestApplyConfirmIgnoresReportFromBeforeTheRollback(t *testing.T) {
	f, online, cfg := cutOffFixture(t, true)
	svc := &reportClock{fakeService: f.svc, reportedAt: time.Now().Add(-time.Hour)}
	cfg.Service = svc
	if res := f.applyWith(cfg); res.Status != StatusUnconfirmed {
		t.Fatalf("first run %+v", res)
	}
	*online = true
	f.confirmRequest()
	if res := f.applyWith(cfg); res.Status != StatusUnconfirmed {
		t.Fatalf("stale report confirmed the rollback: %+v", res)
	}
	svc.reportedAt = time.Now()
	f.confirmRequest()
	if res := f.applyWith(cfg); res.Status != StatusRolledBack {
		t.Fatalf("fresh report: %+v", res)
	}
}

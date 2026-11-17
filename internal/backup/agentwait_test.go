package backup

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
)

// reconnectingHost is an agent host that connects after a number of
// lookups, or never with a negative number.
type reconnectingHost struct {
	*fakeHost
	mu      sync.Mutex
	offline int
	picks   int
}

func (h *reconnectingHost) Pick(string) (hostBackupTarget, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.picks++
	if h.offline < 0 || h.picks <= h.offline {
		return nil, host.ErrAgentOffline
	}
	return h.fakeHost, nil
}

// fakeAfter fires at once and adds up how long the run would have waited.
type fakeAfter struct {
	mu     sync.Mutex
	waited time.Duration
}

func (f *fakeAfter) after(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.waited += d
	f.mu.Unlock()
	c := make(chan time.Time, 1)
	c <- time.Time{}
	return c
}

func agentWaitTest(t *testing.T, offline int) (*Service, *Job, *fakeAfter) {
	t.Helper()
	svc, fake, root := newExecTest(t, false)
	svc.SetHostResolver(&reconnectingHost{fakeHost: fake, offline: offline})
	clock := &fakeAfter{}
	svc.exec.after = clock.after
	job, err := svc.CreateJob(context.Background(), volumeJob("agent-job", root))
	if err != nil {
		t.Fatal(err)
	}
	return svc, job, clock
}

func lastRun(t *testing.T, svc *Service) *Run {
	t.Helper()
	runs, err := svc.ListRuns(context.Background(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	return &runs[0]
}

func runLogText(t *testing.T, svc *Service, runID int64) string {
	t.Helper()
	entries, err := svc.store.listRunLogs(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Line + "\n")
	}
	return b.String()
}

func TestScheduledRunWaitsForReconnectingAgent(t *testing.T) {
	svc, job, clock := agentWaitTest(t, 3)
	svc.runScheduled(job.ID, job.Name)
	run := lastRun(t, svc)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s (%s), want success", run.Status, run.Error)
	}
	if clock.waited != 3*agentPoll {
		t.Errorf("waited %s, want %s", clock.waited, 3*agentPoll)
	}
	if log := runLogText(t, svc, run.ID); !strings.Contains(log, "waiting up to 5m0s") || !strings.Contains(log, "connected") {
		t.Errorf("run log lacks the wait:\n%s", log)
	}
}

func TestScheduledRunFailsWhenAgentStaysOffline(t *testing.T) {
	svc, job, clock := agentWaitTest(t, -1)
	svc.runScheduled(job.ID, job.Name)
	run := lastRun(t, svc)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "agent offline") {
		t.Fatalf("status %s (%s), want failed with agent offline", run.Status, run.Error)
	}
	if clock.waited != agentWait {
		t.Errorf("waited %s, want %s", clock.waited, agentWait)
	}
}

// A run someone started by hand fails at once: the user sees the reason.
func TestManualRunDoesNotWaitForAgent(t *testing.T) {
	svc, job, clock := agentWaitTest(t, -1)
	run, err := svc.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status %s, want failed", run.Status)
	}
	if clock.waited != 0 {
		t.Errorf("waited %s, want no wait", clock.waited)
	}
}

func TestScheduledWaitEndsWhenRunIsCancelled(t *testing.T) {
	svc, job, _ := agentWaitTest(t, -1)
	ctx, cancel := context.WithCancel(withHostWait(context.Background()))
	svc.exec.after = func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}
	run, err := svc.exec.Run(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status %s, want failed", run.Status)
	}
}

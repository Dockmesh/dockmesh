package gitsource

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSync blocks every run until release is closed or fed, and records
// how many runs overlapped per stack.
type fakeSync struct {
	release chan struct{}
	started chan string

	mu        sync.Mutex
	running   map[string]int
	maxActive int
	runs      atomic.Int32
}

func newFakeSync() *fakeSync {
	return &fakeSync{release: make(chan struct{}), started: make(chan string, 64), running: map[string]int{}}
}

func (f *fakeSync) run(ctx context.Context, stack string) (*SyncResult, error) {
	f.mu.Lock()
	f.running[stack]++
	if f.running[stack] > f.maxActive {
		f.maxActive = f.running[stack]
	}
	f.mu.Unlock()
	f.runs.Add(1)
	f.started <- stack
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	f.mu.Lock()
	f.running[stack]--
	f.mu.Unlock()
	return &SyncResult{}, nil
}

func waitStarted(t *testing.T, f *fakeSync) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sync did not start")
	}
}

func TestSyncSerialisedPerStack(t *testing.T) {
	s := New(nil, nil, nil, t.TempDir(), nil)
	f := newFakeSync()
	s.syncFn = f.run

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Sync(context.Background(), "web")
		}()
	}
	for i := 0; i < 4; i++ {
		waitStarted(t, f)
		f.release <- struct{}{}
	}
	wg.Wait()
	if f.maxActive != 1 {
		t.Fatalf("max concurrent syncs of one stack = %d, want 1", f.maxActive)
	}
}

func TestTriggerSyncCoalesces(t *testing.T) {
	tests := []struct {
		name     string
		extra    int // deliveries while the first sync runs
		wantRuns int32
	}{
		{"single delivery", 0, 1},
		{"one delivery while running", 1, 2},
		{"replayed deliveries collapse", 10, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil, nil, nil, t.TempDir(), nil)
			f := newFakeSync()
			s.syncFn = f.run

			reqCtx, cancelReq := context.WithCancel(context.Background())
			s.TriggerSync(reqCtx, "web")
			cancelReq()
			waitStarted(t, f)
			for i := 0; i < tc.extra; i++ {
				s.TriggerSync(context.Background(), "web")
			}
			// A cancelled request context must not end the sync early.
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			still := f.running["web"]
			f.mu.Unlock()
			if still != 1 {
				t.Fatal("sync ended with the request context")
			}
			f.release <- struct{}{}
			for i := int32(1); i < tc.wantRuns; i++ {
				waitStarted(t, f)
				f.release <- struct{}{}
			}

			deadline := time.Now().Add(2 * time.Second)
			for {
				s.syncMu.Lock()
				_, active := s.triggered["web"]
				s.syncMu.Unlock()
				if !active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("trigger loop did not finish")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if got := f.runs.Load(); got != tc.wantRuns {
				t.Fatalf("runs = %d, want %d", got, tc.wantRuns)
			}
			if f.maxActive != 1 {
				t.Fatalf("max concurrent syncs = %d, want 1", f.maxActive)
			}
		})
	}
}

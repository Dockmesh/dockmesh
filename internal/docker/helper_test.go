package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// fakeHelper is a helper container that only exits once it is killed or
// its output was fully read (finished).
type fakeHelper struct {
	finished bool
	killed   bool
	removed  bool
}

func (f *fakeHelper) ContainerKill(context.Context, string, client.ContainerKillOptions) (client.ContainerKillResult, error) {
	f.killed = true
	return client.ContainerKillResult{}, nil
}

func (f *fakeHelper) ContainerWait(ctx context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	status := make(chan container.WaitResponse, 1)
	errs := make(chan error, 1)
	switch {
	case f.killed:
		status <- container.WaitResponse{StatusCode: 137}
	case f.finished:
		status <- container.WaitResponse{StatusCode: 0}
	default:
		go func() { <-ctx.Done(); errs <- ctx.Err() }()
	}
	return client.ContainerWaitResult{Result: status, Error: errs}
}

func (f *fakeHelper) ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removed = true
	return client.ContainerRemoveResult{}, nil
}

func TestFinishHelper(t *testing.T) {
	tests := []struct {
		name       string
		finished   bool
		aborted    bool
		wantKill   bool
		wantStatus int64
	}{
		{"finished", true, false, false, 0},
		{"reader went away", false, true, true, 137},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeHelper{finished: tt.finished}
			// The run's context is gone; cleanup must still happen.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			status, err := FinishHelper(ctx, f, "h1", tt.aborted)
			if err != nil || status != tt.wantStatus {
				t.Fatalf("status %d err %v, want %d", status, err, tt.wantStatus)
			}
			if f.killed != tt.wantKill || !f.removed {
				t.Fatalf("killed %v removed %v", f.killed, f.removed)
			}
		})
	}
}

type fakeSweep struct {
	items   []container.Summary
	listed  client.Filters
	failIDs map[string]bool
	removed []string
}

func (f *fakeSweep) ContainerList(_ context.Context, o client.ContainerListOptions) (client.ContainerListResult, error) {
	f.listed = o.Filters
	return client.ContainerListResult{Items: f.items}, nil
}

func (f *fakeSweep) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if f.failIDs[id] {
		return client.ContainerRemoveResult{}, errors.New("conflict")
	}
	f.removed = append(f.removed, id)
	return client.ContainerRemoveResult{}, nil
}

func TestRemoveOrphanHelpers(t *testing.T) {
	start := time.Unix(1_800_000_000, 500_000_000)
	helper := map[string]string{"dockmesh.managed": "true", "dockmesh.component": BackupHelperComponent}
	before := start.Unix() - 60
	tests := []struct {
		name    string
		c       container.Summary
		removed bool
	}{
		{"exited before start", container.Summary{ID: "a", Labels: helper, State: container.StateExited, Created: before}, true},
		{"created but never started", container.Summary{ID: "a", Labels: helper, State: container.StateCreated, Created: before}, true},
		{"dead", container.Summary{ID: "a", Labels: helper, State: container.StateDead, Created: before}, true},
		{"running, another process may use it", container.Summary{ID: "a", Labels: helper, State: container.StateRunning, Created: before}, false},
		{"paused", container.Summary{ID: "a", Labels: helper, State: container.StatePaused, Created: before}, false},
		{"created in the start second", container.Summary{ID: "a", Labels: helper, State: container.StateExited, Created: start.Unix()}, false},
		{"created after start", container.Summary{ID: "a", Labels: helper, State: container.StateExited, Created: start.Unix() + 5}, false},
		{"other component", container.Summary{ID: "a", Labels: map[string]string{"dockmesh.component": "stack-files-helper"}, State: container.StateExited, Created: before}, false},
		{"no label", container.Summary{ID: "a", Image: "busybox", State: container.StateExited, Created: before}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSweep{items: []container.Summary{tt.c}}
			n, err := RemoveOrphanHelpers(context.Background(), f, start)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(f.removed) == 1; got != tt.removed || n != len(f.removed) {
				t.Fatalf("removed = %v (n=%d), want removed %v", f.removed, n, tt.removed)
			}
			if v := f.listed["label"]; len(v) != 1 || !v["dockmesh.component="+BackupHelperComponent] {
				t.Fatalf("list filters = %v", f.listed)
			}
		})
	}
}

func TestRemoveOrphanHelpersContinuesAfterError(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	helper := map[string]string{"dockmesh.component": BackupHelperComponent}
	f := &fakeSweep{
		items: []container.Summary{
			{ID: "a", Labels: helper, State: container.StateExited, Created: 1},
			{ID: "b", Labels: helper, State: container.StateExited, Created: 1},
		},
		failIDs: map[string]bool{"a": true},
	}
	n, err := RemoveOrphanHelpers(context.Background(), f, start)
	if err == nil || n != 1 || len(f.removed) != 1 || f.removed[0] != "b" {
		t.Fatalf("n=%d removed=%v err=%v", n, f.removed, err)
	}
}

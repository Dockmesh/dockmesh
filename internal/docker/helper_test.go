package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// fakeHelper is a helper container that only exits once it is killed or
// its output was fully read (finished).
type fakeHelper struct {
	finished bool
	killed   bool
	removed  bool
}

func (f *fakeHelper) ContainerKill(context.Context, string, string) error {
	f.killed = true
	return nil
}

func (f *fakeHelper) ContainerWait(ctx context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
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
	return status, errs
}

func (f *fakeHelper) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	f.removed = true
	return nil
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

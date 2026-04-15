package host

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/system"
	dtypes "github.com/docker/docker/api/types"
)

// fakeHost implements the Host interface for fan-out tests. Its behavior
// on ListContainers is configurable: return a fixed row set, an error,
// or block past a deadline. All other Host methods panic if called —
// the tests only exercise ListContainers.
type fakeHost struct {
	id      string
	name    string
	rows    []dtypes.Container
	err     error
	delay   time.Duration // 0 = return immediately
}

func (f *fakeHost) ID() string   { return f.id }
func (f *fakeHost) Name() string { return f.name }

func (f *fakeHost) ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// Stubs — panic on call so the test suite catches accidental use of
// methods outside the exercised surface. If we later add more fan-out
// targets, update the fake accordingly.
func (f *fakeHost) InspectContainer(context.Context, string) (dtypes.ContainerJSON, error) {
	panic("not implemented for fake")
}
func (f *fakeHost) StartContainer(context.Context, string) error   { panic("no") }
func (f *fakeHost) StopContainer(context.Context, string) error    { panic("no") }
func (f *fakeHost) RestartContainer(context.Context, string) error { panic("no") }
func (f *fakeHost) RemoveContainer(context.Context, string, bool) error {
	panic("no")
}
func (f *fakeHost) ContainerLogs(context.Context, string, string, bool) (io.ReadCloser, error) {
	panic("no")
}
func (f *fakeHost) ContainerStats(context.Context, string) (io.ReadCloser, error) {
	panic("no")
}
func (f *fakeHost) StartExec(context.Context, string, []string) (ExecSession, error) {
	panic("no")
}
func (f *fakeHost) ListImages(context.Context, bool) ([]dtypes.ImageSummary, error) {
	panic("no")
}
func (f *fakeHost) ListNetworks(context.Context) ([]dtypes.NetworkResource, error) {
	panic("no")
}
func (f *fakeHost) ListVolumes(context.Context) ([]any, error) { panic("no") }
func (f *fakeHost) DeployStack(context.Context, string, string, string) (*compose.DeployResult, error) {
	panic("no")
}
func (f *fakeHost) StopStack(context.Context, string) error { panic("no") }
func (f *fakeHost) StackStatus(context.Context, string) ([]compose.StatusEntry, error) {
	panic("no")
}
func (f *fakeHost) SystemMetrics(context.Context) (system.Metrics, error) {
	return system.Metrics{}, nil
}

func TestFanOut_HappyPath(t *testing.T) {
	hosts := []Host{
		&fakeHost{id: "local", name: "Local", rows: []dtypes.Container{{ID: "a1"}, {ID: "a2"}}},
		&fakeHost{id: "agent1", name: "agent1", rows: []dtypes.Container{{ID: "b1"}}},
	}
	res := FanOut(context.Background(), hosts, func(ctx context.Context, h Host) ([]dtypes.Container, error) {
		return h.ListContainers(ctx, true)
	})
	if len(res.Items) != 3 {
		t.Fatalf("expected 3 merged rows, got %d", len(res.Items))
	}
	if len(res.Unreachable) != 0 {
		t.Fatalf("expected 0 unreachable, got %d", len(res.Unreachable))
	}
}

func TestFanOut_PartialFailure(t *testing.T) {
	hosts := []Host{
		&fakeHost{id: "local", name: "Local", rows: []dtypes.Container{{ID: "ok"}}},
		&fakeHost{id: "bad", name: "bad-agent", err: errors.New("connection refused")},
	}
	res := FanOut(context.Background(), hosts, func(ctx context.Context, h Host) ([]dtypes.Container, error) {
		return h.ListContainers(ctx, true)
	})
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 row from the working host, got %d", len(res.Items))
	}
	if len(res.Unreachable) != 1 {
		t.Fatalf("expected 1 unreachable entry, got %d", len(res.Unreachable))
	}
	if res.Unreachable[0].HostID != "bad" || res.Unreachable[0].Reason != "connection refused" {
		t.Errorf("unreachable entry wrong: %+v", res.Unreachable[0])
	}
}

func TestFanOutTimeout_SlowHostExcluded(t *testing.T) {
	hosts := []Host{
		&fakeHost{id: "fast", name: "fast", rows: []dtypes.Container{{ID: "quick"}}},
		&fakeHost{id: "slow", name: "slow", delay: 200 * time.Millisecond},
	}
	res := FanOutTimeout(context.Background(), hosts, 50*time.Millisecond, func(ctx context.Context, h Host) ([]dtypes.Container, error) {
		return h.ListContainers(ctx, true)
	})
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 row from the fast host, got %d", len(res.Items))
	}
	if len(res.Unreachable) != 1 || res.Unreachable[0].HostID != "slow" {
		t.Fatalf("expected slow host to be reported unreachable, got %+v", res.Unreachable)
	}
}

func TestFanOut_EmptyHosts(t *testing.T) {
	res := FanOut(context.Background(), nil, func(ctx context.Context, h Host) ([]dtypes.Container, error) {
		return nil, nil
	})
	if len(res.Items) != 0 || len(res.Unreachable) != 0 {
		t.Errorf("empty fan-out should yield empty result, got %+v", res)
	}
	// Items and Unreachable must be non-nil so JSON encoding produces
	// [] not null.
	if res.Items == nil {
		t.Error("Items is nil; should be empty slice for JSON compat")
	}
	if res.Unreachable == nil {
		t.Error("Unreachable is nil; should be empty slice for JSON compat")
	}
}

func TestIsAll(t *testing.T) {
	cases := map[string]bool{
		"":       false,
		"local":  false,
		"agent1": false,
		"all":    true,
		"All":    false, // case sensitive
	}
	for in, want := range cases {
		if got := IsAll(in); got != want {
			t.Errorf("IsAll(%q) = %v, want %v", in, got, want)
		}
	}
}

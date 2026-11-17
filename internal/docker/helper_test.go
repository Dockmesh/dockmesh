package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
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
	items       []container.Summary
	imageLabels map[string]map[string]string
	listed      client.Filters
	failIDs     map[string]bool
	removed     []string
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

func (f *fakeSweep) ImageInspect(_ context.Context, id string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	var r client.ImageInspectResult
	r.Config = &dockerspec.DockerOCIImageConfig{}
	r.Config.Labels = f.imageLabels[id]
	return r, nil
}

func TestRemoveOrphanHelpers(t *testing.T) {
	start := time.Unix(1_800_000_000, 500_000_000)
	before := start.Unix() - 60
	labels := func(extra ...string) map[string]string {
		l := map[string]string{"dockmesh.managed": "true", "dockmesh.component": BackupHelperComponent}
		for i := 0; i+1 < len(extra); i += 2 {
			l[extra[i]] = extra[i+1]
		}
		return l
	}
	server := labels(HelperOwnerLabel, HelperOwnerServer)
	agent := labels(HelperOwnerLabel, HelperOwnerAgent)
	tests := []struct {
		name    string
		owner   string
		c       container.Summary
		removed bool
	}{
		{"exited before start", HelperOwnerServer, container.Summary{Labels: server, State: container.StateExited, Created: before}, true},
		{"created but never started", HelperOwnerServer, container.Summary{Labels: server, State: container.StateCreated, Created: before}, true},
		{"dead", HelperOwnerServer, container.Summary{Labels: server, State: container.StateDead, Created: before}, true},
		{"running", HelperOwnerServer, container.Summary{Labels: server, State: container.StateRunning, Created: before}, false},
		{"paused", HelperOwnerServer, container.Summary{Labels: server, State: container.StatePaused, Created: before}, false},
		{"created in the start second", HelperOwnerServer, container.Summary{Labels: server, State: container.StateExited, Created: start.Unix()}, false},
		{"created after start", HelperOwnerServer, container.Summary{Labels: server, State: container.StateExited, Created: start.Unix() + 5}, false},
		{"server keeps an agent's helper", HelperOwnerServer, container.Summary{Labels: agent, State: container.StateExited, Created: before}, false},
		{"agent keeps the server's helper", HelperOwnerAgent, container.Summary{Labels: server, State: container.StateExited, Created: before}, false},
		{"agent removes its own", HelperOwnerAgent, container.Summary{Labels: agent, State: container.StateExited, Created: before}, true},
		{"server removes one without owner", HelperOwnerServer, container.Summary{Labels: labels(), State: container.StateExited, Created: before}, true},
		{"agent keeps one without owner", HelperOwnerAgent, container.Summary{Labels: labels(), State: container.StateExited, Created: before}, false},
		{"part of a compose project", HelperOwnerServer, container.Summary{Labels: labels(HelperOwnerLabel, HelperOwnerServer, "com.docker.compose.project", "app"), State: container.StateExited, Created: before}, false},
		{"label from the image", HelperOwnerServer, container.Summary{ImageID: "sha256:forged", Labels: server, State: container.StateExited, Created: before}, false},
		{"other component", HelperOwnerServer, container.Summary{Labels: map[string]string{"dockmesh.component": "stack-files-helper"}, State: container.StateExited, Created: before}, false},
		{"no label", HelperOwnerServer, container.Summary{Image: "busybox", State: container.StateExited, Created: before}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.ID = "a"
			f := &fakeSweep{
				items:       []container.Summary{tt.c},
				imageLabels: map[string]map[string]string{"sha256:forged": {"dockmesh.component": BackupHelperComponent}},
			}
			n, err := RemoveOrphanHelpers(context.Background(), f, tt.owner, start)
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
	l := BackupHelperLabels(HelperOwnerServer)
	f := &fakeSweep{
		items: []container.Summary{
			{ID: "a", Labels: l, State: container.StateExited, Created: 1},
			{ID: "b", Labels: l, State: container.StateExited, Created: 1},
		},
		failIDs: map[string]bool{"a": true},
	}
	n, err := RemoveOrphanHelpers(context.Background(), f, HelperOwnerServer, start)
	if err == nil || n != 1 || len(f.removed) != 1 || f.removed[0] != "b" {
		t.Fatalf("n=%d removed=%v err=%v", n, f.removed, err)
	}
}

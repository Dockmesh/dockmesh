package backup

import (
	"context"
	"testing"
)

// A run records the host it was taken on; moving the job later doesn't
// change it, and a restore without a host goes there.
func TestRunKeepsItsHost(t *testing.T) {
	svc, _ := newTestService(t, false)
	ctx := context.Background()
	id, err := svc.store.createJob(ctx, JobInput{
		Name: "data", HostID: "agent-1", TargetType: "local",
		Sources: []Source{{Type: "volume", Name: "data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.store.getJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := svc.store.startRun(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.db.ExecContext(ctx, `UPDATE backup_jobs SET host_id = 'local' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	run, err := svc.store.getRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HostID != "agent-1" {
		t.Errorf("run host %q, want agent-1", run.HostID)
	}
	got, err := svc.RunHost(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-1" {
		t.Errorf("RunHost %q, want agent-1", got)
	}
}

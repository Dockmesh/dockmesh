package backup

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// blockingHost holds the volume stream open until release is closed.
type blockingHost struct {
	*fakeHost
	release chan struct{}
}

func (b *blockingHost) Pick(string) (hostBackupTarget, error) { return b, nil }

func (b *blockingHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakeHost.VolumeTar(ctx, name)
}

func waitRun(t *testing.T, svc *Service, id int64) *Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := svc.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusRunning {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %d still running", id)
	return nil
}

func TestStartRunAsyncAndSkipIfRunning(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	blocking := &blockingHost{fakeHost: fake, release: make(chan struct{})}
	svc.SetHostResolver(blocking)
	ctx, cancel := context.WithCancel(context.Background())

	job, err := svc.CreateJob(ctx, volumeJob("async", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusRunning {
		t.Fatalf("StartRun should return the running run, got %s", run.Status)
	}
	// The caller going away must not cancel the run.
	cancel()

	if _, err := svc.StartRun(context.Background(), job.ID); !errors.Is(err, ErrJobRunning) {
		t.Fatalf("second start while running: %v", err)
	}
	svc.runScheduled(job.ID, job.Name)
	runs, _ := svc.ListRuns(context.Background(), 10)
	if len(runs) != 1 {
		t.Fatalf("scheduled fire during a run should be skipped, got %d runs", len(runs))
	}

	close(blocking.release)
	if got := waitRun(t, svc, run.ID); got.Status != StatusSuccess {
		t.Fatalf("async run: %+v", got)
	}
	if _, err := svc.runNow(context.Background(), job.ID); err != nil {
		t.Fatalf("job should be free again: %v", err)
	}
}

func TestStartFailsInterruptedRuns(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("crash", root))
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.exec.begin(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.setRunPath(ctx, id, "jobs/x/partial.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	r, _ := svc.GetRun(ctx, id)
	if r.Status != StatusFailed || r.FinishedAt == nil {
		t.Fatalf("interrupted run: %+v", r)
	}
	partial, _ := svc.store.partialRuns(ctx, job.ID)
	if len(partial) != 1 {
		t.Fatalf("interrupted run's file should be queued for cleanup: %+v", partial)
	}
}

func TestTimezone(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	in := volumeJob("tz", root)
	in.Schedule = "0 3 * * *"
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetTimezone(ctx, "Mars/Olympus"); err == nil {
		t.Fatal("unknown zone accepted")
	}
	for _, zone := range []string{"Asia/Tokyo", "America/New_York"} {
		if err := svc.SetTimezone(ctx, zone); err != nil {
			t.Fatal(err)
		}
		got, _ := svc.GetJob(ctx, job.ID)
		if got.NextRunAt == nil {
			t.Fatal("no next run after changing the zone")
		}
		loc, _ := time.LoadLocation(zone)
		if h := got.NextRunAt.In(loc).Hour(); h != 3 {
			t.Fatalf("%s: next run at %v, want 03:00 local", zone, got.NextRunAt.In(loc))
		}
		if info := svc.ScheduleInfo(); info.Timezone != zone || info.Source != "setting" {
			t.Fatalf("schedule info: %+v", info)
		}
	}
	if err := svc.SetTimezone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if info := svc.ScheduleInfo(); info.Source != "server" {
		t.Fatalf("empty setting should fall back to the server zone: %+v", info)
	}
}

func TestInlineTargetsMigrate(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	db := svc.store.db
	// Two legacy jobs with the same inline config and one with another.
	for _, j := range []struct{ name, cfg string }{
		{"a", `{"path":"` + jsonEscape(root) + `"}`},
		{"b", `{"path":"` + jsonEscape(root) + `"}`},
		{"c", `{"host":"nas","username":"u","host_key":"ssh-ed25519 AAAA"}`},
	} {
		typ := "local"
		if j.name == "c" {
			typ = "sftp"
		}
		if _, err := db.Exec(`INSERT INTO backup_jobs (name, host_id, encrypt, target_type, target_config, sources) VALUES (?, 'fake', 0, ?, ?, '[{"type":"volume","name":"data"}]')`,
			j.name, typ, j.cfg); err != nil {
			t.Fatal(err)
		}
	}
	n, err := svc.store.migrateInlineTargets(ctx)
	if err != nil || n != 3 {
		t.Fatalf("migrated %d: %v", n, err)
	}
	if n, _ := svc.store.migrateInlineTargets(ctx); n != 0 {
		t.Fatalf("second pass migrated %d jobs", n)
	}
	jobs, _ := svc.ListJobs(ctx)
	ids := map[string]int64{}
	for _, j := range jobs {
		if j.TargetID == 0 {
			t.Fatalf("job %s has no target", j.Name)
		}
		ids[j.Name] = j.TargetID
	}
	if ids["a"] != ids["b"] || ids["a"] == ids["c"] {
		t.Fatalf("identical configs should share a target: %v", ids)
	}
	st := targets.NewTargetStore(db)
	sftp, err := st.Get(ctx, ids["c"])
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := sftp.Config.(map[string]any); cfg["host_key"] != "ssh-ed25519 AAAA" {
		t.Fatalf("recorded host key lost: %v", sftp.Config)
	}
	if err := st.Delete(ctx, ids["a"]); !errors.Is(err, targets.ErrTargetInUse) {
		t.Fatalf("deleting a used target: %v", err)
	}

	// The migrated jobs still run.
	var a Job
	for _, j := range jobs {
		if j.Name == "a" {
			a = j
		}
	}
	if r, err := svc.runNow(ctx, a.ID); err != nil || r.Status != StatusSuccess {
		t.Fatalf("run after migration: %v %+v", err, r)
	}
}

func TestJobWithSavedTarget(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	st := targets.NewTargetStore(svc.store.db)
	tgt, err := st.Create(ctx, targets.TargetInput{Name: "disk", Type: "local", Config: map[string]any{"path": root}})
	if err != nil {
		t.Fatal(err)
	}
	in := volumeJob("saved", root)
	in.TargetID, in.TargetType, in.TargetConfig = tgt.ID, "", nil
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if job.TargetID != tgt.ID || job.TargetType != "local" {
		t.Fatalf("job target: %+v", job)
	}
	in.TargetID = 9999
	if _, err := svc.CreateJob(ctx, in); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	// An inline target becomes a saved one named after the job.
	inline, err := svc.CreateJob(ctx, volumeJob("inline", root))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.Get(ctx, inline.TargetID)
	if err != nil || saved.Name != "inline" {
		t.Fatalf("inline target not saved: %v %+v", err, saved)
	}
}

func jsonEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '"' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

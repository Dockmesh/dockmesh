package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/operations"
)

// stallTarget wraps a real target; its writes, or its reads after the
// first chunk, block until the writer or reader is closed, like a server
// that accepted the connection and then stopped answering.
type stallTarget struct {
	targets.Target
	stallWrite, stallRead bool
}

func (s *stallTarget) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	w, err := s.Target.Open(ctx, path)
	if err != nil || !s.stallWrite {
		return w, err
	}
	return &stallWriter{w: w, closed: make(chan struct{})}, nil
}

func (s *stallTarget) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	rc, err := s.Target.Read(ctx, path)
	if err != nil || !s.stallRead {
		return rc, err
	}
	return newStallReader(rc), nil
}

type stallWriter struct {
	w      io.WriteCloser
	once   sync.Once
	closed chan struct{}
}

func (s *stallWriter) Write([]byte) (int, error) {
	<-s.closed
	return 0, errors.New("connection closed")
}

func (s *stallWriter) Close() error {
	s.once.Do(func() { close(s.closed) })
	return s.w.Close()
}

// stallReader returns one small chunk, then blocks until closed.
type stallReader struct {
	r      io.ReadCloser
	once   sync.Once
	closed chan struct{}
	read   bool
}

func newStallReader(r io.ReadCloser) *stallReader {
	return &stallReader{r: r, closed: make(chan struct{})}
}

func (s *stallReader) Read(p []byte) (int, error) {
	if s.read {
		<-s.closed
		return 0, errors.New("connection closed")
	}
	s.read = true
	if len(p) > 16 {
		p = p[:16]
	}
	return s.r.Read(p)
}

func (s *stallReader) Close() error {
	s.once.Do(func() { close(s.closed) })
	return s.r.Close()
}

// useStallTarget makes every target built from now on a stallTarget.
func useStallTarget(t *testing.T, st *stallTarget) {
	t.Helper()
	prev := wrapRunTarget
	wrapRunTarget = func(inner targets.Target) targets.Target {
		st.Target = inner
		return st
	}
	t.Cleanup(func() { wrapRunTarget = prev })
}

// hookHost records hooks; the hook in container "slow" blocks until its
// context ends, the one in "slowpost" until release is closed. With
// stallSource the volume stream stops after its first chunk.
type hookHost struct {
	*fakeHost
	stallSource bool
	release     chan struct{}

	mu       sync.Mutex
	ran      []string
	postErrs []error
}

func newHookHost(f *fakeHost) *hookHost { return &hookHost{fakeHost: f, release: make(chan struct{})} }

func (h *hookHost) Pick(string) (hostBackupTarget, error) { return h, nil }

func (h *hookHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, err := h.fakeHost.VolumeTar(ctx, name)
	if err != nil || !h.stallSource {
		return rc, err
	}
	return newStallReader(rc), nil
}

func (h *hookHost) ContainerExec(ctx context.Context, container string, _ []string) ([]byte, int, error) {
	switch container {
	case "slow":
		<-ctx.Done()
		return nil, -1, ctx.Err()
	case "slowpost":
		<-h.release
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ran = append(h.ran, container)
	if container == "slowpost" || container == "post" {
		h.postErrs = append(h.postErrs, ctx.Err())
	}
	return nil, 0, nil
}

func (h *hookHost) record() ([]string, []error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ran...), append([]error(nil), h.postErrs...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runOp(t *testing.T, ops *operations.Service, runID int64) operations.Operation {
	t.Helper()
	list, err := ops.List(context.Background(), operations.Filter{Ref: operations.BackupRunRef(runID)})
	if err != nil || len(list) == 0 {
		t.Fatalf("operation of run %d: %v %d", runID, err, len(list))
	}
	return list[0]
}

func stepOf(op operations.Operation, key string) operations.Step {
	for _, s := range op.Steps {
		if s.Key == key {
			return s
		}
	}
	return operations.Step{}
}

// finishedRun waits until the background run ended and its slot is free.
func finishedRun(t *testing.T, svc *Service, id int64) *Run {
	t.Helper()
	done := make(chan struct{})
	go func() { svc.runs.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end")
	}
	run, err := svc.GetRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCancelBackupRunAtEachStep(t *testing.T) {
	cases := []struct {
		name        string
		pre, post   []Hook
		stallSource bool
		target      *stallTarget
		step        string
		// wantCancelled is false where the cancel came too late to stop
		// the run: the archive was already complete.
		wantCancelled bool
		wantHooks     string
	}{
		{
			name: "pre-hooks", step: StepPreHooks, wantCancelled: true,
			pre:  []Hook{{Container: "ok", Cmd: []string{"true"}}, {Container: "slow", Cmd: []string{"sleep"}}},
			post: []Hook{{Container: "post", Cmd: []string{"true"}}}, wantHooks: "ok,post",
		},
		{
			name: "archive, source stalled", step: StepArchive, wantCancelled: true, stallSource: true,
			pre:  []Hook{{Container: "ok", Cmd: []string{"true"}}},
			post: []Hook{{Container: "post", Cmd: []string{"true"}}}, wantHooks: "ok,post",
		},
		{
			name: "archive, target stalled", step: StepArchive, wantCancelled: true,
			target: &stallTarget{stallWrite: true},
			post:   []Hook{{Container: "post", Cmd: []string{"true"}}}, wantHooks: "post",
		},
		{
			name: "verify", step: StepVerify, wantCancelled: true,
			target: &stallTarget{stallRead: true},
		},
		{
			name: "post-hooks", step: StepPostHooks,
			post: []Hook{{Container: "slowpost", Cmd: []string{"true"}}}, wantHooks: "slowpost",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, fake, root := newExecTest(t, false)
			ops := operations.New(svc.store.db)
			svc.SetOperations(ops)
			host := newHookHost(fake)
			host.stallSource = c.stallSource
			svc.SetHostResolver(host)
			if c.target != nil {
				useStallTarget(t, c.target)
			}
			ctx := context.Background()
			in := volumeJob("cancel", root)
			in.PreHooks, in.PostHooks = c.pre, c.post
			job, err := svc.CreateJob(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.StartRun(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var opID string
			waitFor(t, c.step+" running and cancellable", func() bool {
				op := runOp(t, ops, run.ID)
				opID = op.ID
				return op.Cancellable && stepOf(op, c.step).Status == operations.StepRunning
			})
			if err := ops.Cancel(opID); err != nil {
				t.Fatalf("cancel: %v", err)
			}
			if err := ops.Cancel(opID); !errors.Is(err, operations.ErrNotCancellable) {
				t.Fatalf("second cancel: %v", err)
			}
			close(host.release)
			got := finishedRun(t, svc, run.ID)
			op := runOp(t, ops, run.ID)

			if c.wantCancelled {
				if got.Status != StatusFailed || !strings.Contains(got.Error, "cancelled by a user") {
					t.Fatalf("run %s %q, want failed with the cancel", got.Status, got.Error)
				}
				if op.Status != operations.StatusCancelled || !strings.Contains(op.Error, stepOf(op, c.step).Label) {
					t.Fatalf("operation %s %q, want cancelled naming the step", op.Status, op.Error)
				}
				if s := stepOf(op, c.step); s.Status != operations.StepCancelled {
					t.Fatalf("step %s: %s, want cancelled", c.step, s.Status)
				}
				if left := archives(t, root); len(left) != 0 {
					t.Fatalf("partial archive left on the target: %v", left)
				}
				if s := stepOf(op, StepRetention); s.Status != operations.StepSkipped {
					t.Fatalf("retention after a cancelled run: %s", s.Status)
				}
			} else if got.Status != StatusSuccess || op.Status != operations.StatusSucceeded {
				t.Fatalf("run %s op %s, want success: the archive was complete", got.Status, op.Status)
			}
			ran, postErrs := host.record()
			if strings.Join(ran, ",") != c.wantHooks {
				t.Fatalf("hooks ran %v, want %s", ran, c.wantHooks)
			}
			for _, err := range postErrs {
				if err != nil {
					t.Fatalf("post-hook ran with a cancelled context: %v", err)
				}
			}
			if !svc.claim(job.ID) {
				t.Fatal("job still marked running after the cancelled run")
			}
			svc.release(job.ID)
		})
	}
}

func TestIdleTimeoutFailsStalledRun(t *testing.T) {
	prev := idleTimeout
	idleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { idleTimeout = prev })

	for _, c := range []struct {
		name        string
		target      *stallTarget
		stallSource bool
	}{
		{name: "target stops taking data", target: &stallTarget{stallWrite: true}},
		{name: "target stops returning data on read-back", target: &stallTarget{stallRead: true}},
		{name: "source stops sending", stallSource: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc, fake, root := newExecTest(t, false)
			ops := operations.New(svc.store.db)
			svc.SetOperations(ops)
			host := newHookHost(fake)
			host.stallSource = c.stallSource
			svc.SetHostResolver(host)
			if c.target != nil {
				useStallTarget(t, c.target)
			}
			ctx := context.Background()
			job, err := svc.CreateJob(ctx, volumeJob("idle", root))
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.StartRun(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := finishedRun(t, svc, run.ID)
			if got.Status != StatusFailed || !strings.Contains(got.Error, "target stalled") {
				t.Fatalf("run %s %q, want failed: target stalled", got.Status, got.Error)
			}
			if op := runOp(t, ops, run.ID); op.Status != operations.StatusFailed || !strings.Contains(op.Error, "target stalled") {
				t.Fatalf("operation %s %q", op.Status, op.Error)
			}
			if left := archives(t, root); len(left) != 0 {
				t.Fatalf("partial archive left: %v", left)
			}
			if !svc.claim(job.ID) {
				t.Fatal("run slot not freed after the stall")
			}
			svc.release(job.ID)
		})
	}
}

// dripHost sends the volume in small pieces with pauses shorter than the
// idle timeout, for longer than the timeout in total.
type dripHost struct{ *fakeHost }

func (d dripHost) Pick(string) (hostBackupTarget, error) { return d, nil }

func (d dripHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, err := d.fakeHost.VolumeTar(ctx, name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(&dripReader{r: rc}), nil
}

type dripReader struct {
	r io.Reader
	n int
}

func (d *dripReader) Read(p []byte) (int, error) {
	if d.n < 8 {
		d.n++
		time.Sleep(50 * time.Millisecond)
		if len(p) > 64 {
			p = p[:64]
		}
	}
	return d.r.Read(p)
}

func TestIdleTimeoutKeepsMovingTransfer(t *testing.T) {
	prev := idleTimeout
	idleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { idleTimeout = prev })
	svc, fake, root := newExecTest(t, false)
	svc.SetHostResolver(dripHost{fake})
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("slow", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("slow but moving transfer: %v %+v", err, run)
	}
}

func TestCancelRestoreWhileDownloading(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	host := newImportHost(fake)
	svc.SetHostResolver(host)
	ops := operations.New(svc.store.db)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("v", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("%v %+v", err, run)
	}
	useStallTarget(t, &stallTarget{stallRead: true})
	op := ops.Start(ctx, operations.Spec{Kind: operations.KindBackupRestore, Target: "data", Steps: RestoreSteps})
	done := make(chan error, 1)
	go func() {
		_, err := svc.Restore(operations.WithOp(ctx, op), run.ID, RestoreOptions{Replace: true})
		op.Finish(err)
		done <- err
	}()
	waitFor(t, "download running", func() bool {
		got, _ := ops.Get(ctx, op.ID())
		return got != nil && got.Cancellable && stepOf(*got, StepRestoreDownload).Status == operations.StepRunning
	})
	if err := ops.Cancel(op.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("restore did not stop")
	}
	if !errors.Is(err, operations.ErrCancelled) {
		t.Fatalf("restore: %v, want cancelled", err)
	}
	if len(host.imported) != 0 {
		t.Fatal("a cancelled restore wrote to the volume")
	}
	got, _ := ops.Get(ctx, op.ID())
	if got.Status != operations.StatusCancelled || stepOf(*got, StepRestoreWrite).Status != operations.StepSkipped ||
		stepOf(*got, StepRestoreDownload).Status != operations.StepCancelled {
		t.Fatalf("operation %+v", got)
	}
}

// gatedImport holds the import until gate is closed.
type gatedImport struct {
	*importHost
	gate, entered chan struct{}
}

func (g *gatedImport) Pick(string) (hostBackupTarget, error) { return g, nil }

func (g *gatedImport) VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, need int64) error {
	close(g.entered)
	<-g.gate
	return g.importHost.VolumeImport(ctx, name, src, replace, need)
}

func TestRestoreNotCancellableWhileWriting(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	host := &gatedImport{importHost: newImportHost(fake), gate: make(chan struct{}), entered: make(chan struct{})}
	svc.SetHostResolver(host)
	ops := operations.New(svc.store.db)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("v", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	op := ops.Start(ctx, operations.Spec{Kind: operations.KindBackupRestore, Target: "data", Steps: RestoreSteps})
	done := make(chan error, 1)
	go func() {
		_, err := svc.Restore(operations.WithOp(ctx, op), run.ID, RestoreOptions{Replace: true})
		done <- err
	}()
	<-host.entered
	if err := ops.Cancel(op.ID()); !errors.Is(err, operations.ErrNotCancellable) {
		t.Fatalf("cancel while writing: %v, want not cancellable", err)
	}
	if got, _ := ops.Get(ctx, op.ID()); got.Cancellable {
		t.Fatal("operation offers cancel while writing")
	}
	close(host.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// needHost records the disk space a restore asks the volume for.
type needHost struct {
	*importHost
	need int64
}

func (n *needHost) Pick(string) (hostBackupTarget, error) { return n, nil }

func (n *needHost) VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, need int64) error {
	n.need = need
	return n.importHost.VolumeImport(ctx, name, src, replace, need)
}

func TestRestorePassesDiskNeed(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	rec := &needHost{importHost: newImportHost(fake)}
	svc.SetHostResolver(rec)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("v", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Restore(ctx, run.ID, RestoreOptions{Replace: true}); err != nil {
		t.Fatal(err)
	}
	// The volume holds one file of 30000 bytes: payload plus a block.
	if want := int64(30000 + 4096); rec.need != want {
		t.Fatalf("need %d, want %d", rec.need, want)
	}
}

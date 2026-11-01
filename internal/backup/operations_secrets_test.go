package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/operations"
)

// echoHost prints the secrets from hook output and fails imports with
// them, like a misbehaving hook or a target that echoes credentials.
type echoHost struct {
	*importHost
	out string
}

func (h *echoHost) Pick(string) (hostBackupTarget, error) { return h, nil }

func (h *echoHost) ContainerExec(context.Context, string, []string) ([]byte, int, error) {
	return []byte(h.out), 0, nil
}

func (h *echoHost) VolumeImport(_ context.Context, name string, src io.Reader, _ bool, _ int64) error {
	_, _ = io.Copy(io.Discard, src)
	return errors.New("import " + name + " refused: " + h.out)
}

func TestOperationOutputNeverHoldsSecrets(t *testing.T) {
	const (
		targetSecret = "Target-Passphrase-4711"
		hookSecret   = "hook-printed-token-0815"
	)
	svc, fake, root := newExecTest(t, false)
	ops := operations.New(svc.store.db)
	svc.SetOperations(ops)
	host := &echoHost{importHost: newImportHost(fake),
		out: "dumping with " + targetSecret + "\npassword=" + hookSecret + "\nAuthorization: Bearer " + hookSecret}
	svc.SetHostResolver(host)
	events, unsub := ops.Subscribe()
	defer unsub()

	ctx := operations.WithActor(context.Background(), "u1")
	in := volumeJob("secrets", root)
	in.TargetConfig = map[string]any{"path": root, "passphrase": targetSecret}
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	job.PreHooks = []Hook{{Container: "db", Cmd: []string{"dump"}}}
	job.PostHooks = []Hook{{Container: "db", Cmd: []string{"start"}}}
	run, err := svc.exec.Run(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusSuccess {
		t.Fatalf("run %s: %s", run.Status, run.Error)
	}

	restore := ops.Start(ctx, operations.Spec{Kind: "backup.restore", Target: "copy", Ref: "backup-run:" + itoa(run.ID), Steps: RestoreSteps})
	_, rerr := svc.Restore(operations.WithOp(ctx, restore), run.ID, RestoreOptions{Target: "copy"})
	restore.Finish(rerr)
	if rerr == nil {
		t.Fatal("restore into the echoing host should fail")
	}

	list, err := ops.List(context.Background(), operations.Filter{Kind: "backup."})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d operations, want run + restore", len(list))
	}
	sawHookLine := false
	for _, o := range list {
		got, err := ops.Get(context.Background(), o.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range got.Log {
			if strings.Contains(l.Text, "dumping with") {
				sawHookLine = true
			}
		}
		assertClean(t, "stored "+got.Kind, got, targetSecret, hookSecret)
	}
	if !sawHookLine {
		t.Fatal("hook output did not reach the operation log")
	}
	for {
		select {
		case ev := <-events:
			assertClean(t, "published "+ev.Op.Kind, &ev.Op, targetSecret, hookSecret)
			if ev.Line != nil && (strings.Contains(ev.Line.Text, targetSecret) || strings.Contains(ev.Line.Text, hookSecret)) {
				t.Fatalf("published line leaks a secret: %q", ev.Line.Text)
			}
		default:
			return
		}
	}
}

func TestRunOperationSteps(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ops := operations.New(svc.store.db)
	svc.SetOperations(ops)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("steps", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.exec.Run(ctx, job)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("%v %+v", err, run)
	}
	list, _ := ops.List(ctx, operations.Filter{Ref: "backup-run:" + itoa(run.ID)})
	if len(list) != 1 {
		t.Fatalf("%d operations for the run", len(list))
	}
	op := list[0]
	if op.Status != operations.StatusSucceeded || op.HostID != "fake" || op.Target != "steps" {
		t.Fatalf("op %+v", op)
	}
	want := map[string]operations.StepStatus{
		StepPrepare: operations.StepSucceeded, StepPreHooks: operations.StepSkipped,
		StepArchive: operations.StepSucceeded, StepVerify: operations.StepSucceeded,
		StepPostHooks: operations.StepSkipped, StepRetention: operations.StepSucceeded,
	}
	for _, st := range op.Steps {
		if st.Status != want[st.Key] {
			t.Errorf("step %s: %s, want %s", st.Key, st.Status, want[st.Key])
		}
	}
}

func TestTargetSecretValues(t *testing.T) {
	tests := []struct {
		typ  string
		cfg  map[string]any
		want []string
	}{
		{"smb", map[string]any{"host": "nas", "username": "u", "password": "smbpw"}, []string{"smbpw"}},
		{"sftp", map[string]any{"host": "h", "password": "sftppw", "private_key": "KEY"}, []string{"sftppw", "KEY"}},
		{"s3", map[string]any{"access_key": "AK", "secret_key": "SK"}, []string{"SK"}},
		{"webdav", map[string]any{"url": "https://x", "password": "davpw"}, []string{"davpw"}},
		{"local", map[string]any{"path": "/b", "passphrase": "pp"}, []string{"pp"}},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			got := strings.Join(targetSecretValues(tt.typ, tt.cfg), ",")
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Fatalf("%s: %q misses %q", tt.typ, got, w)
				}
			}
		})
	}
}

func assertClean(t *testing.T, where string, op *operations.Operation, secrets ...string) {
	t.Helper()
	texts := []string{op.Target, op.Error}
	for _, st := range op.Steps {
		texts = append(texts, st.Detail)
	}
	for _, l := range op.Log {
		texts = append(texts, l.Text)
	}
	for _, s := range texts {
		for _, sec := range secrets {
			if strings.Contains(s, sec) {
				t.Fatalf("%s leaks %q: %q", where, sec, s)
			}
		}
	}
}

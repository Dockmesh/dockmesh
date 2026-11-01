package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
)

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return New(database), database
}

func TestLifecyclePersistsStepsAndLog(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	o := s.Start(ctx, Spec{Kind: "backup.run", Target: "nightly", Ref: "backup-run:7", HostID: "local", Actor: "u1",
		Steps: []StepSpec{{"archive", "Archive and upload"}, {"verify", "Read back and verify"}, {"retention", "Retention"}}})
	o.StepStart("archive", "")
	o.StepProgress("archive", 0.5, "10 MB")
	o.Logf("info", "wrote %d bytes", 10)
	o.StepDone("archive", "")
	o.StepStart("verify", "")
	o.StepFail("verify", errors.New("checksum mismatch"))
	o.Finish(errors.New("checksum mismatch"))

	got, err := s.Get(ctx, o.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || got.Error != "checksum mismatch" {
		t.Fatalf("status %q error %q", got.Status, got.Error)
	}
	want := []StepStatus{StepSucceeded, StepFailed, StepSkipped}
	for i, st := range got.Steps {
		if st.Status != want[i] {
			t.Errorf("step %s: %s, want %s", st.Key, st.Status, want[i])
		}
	}
	if len(got.Log) != 1 || got.Log[0].Text != "wrote 10 bytes" {
		t.Fatalf("log %+v", got.Log)
	}
	if got.FinishedAt == nil || got.StartedAt == nil {
		t.Fatal("times not recorded")
	}
	s.mu.Lock()
	n := len(s.live)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d operations still live after finish", n)
	}
}

func TestLogCapAndLineLimit(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	o := s.Start(ctx, Spec{Kind: "image.update", Target: "web"})
	for i := 0; i < MaxLogLines+50; i++ {
		o.Logf("info", "line %d", i)
	}
	o.Logf("info", "%s", strings.Repeat("é", MaxLineBytes))
	o.Finish(nil)
	got, err := s.Get(ctx, o.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Log) != MaxLogLines {
		t.Fatalf("got %d lines, want %d", len(got.Log), MaxLogLines)
	}
	if got.Log[0].Text != "line 51" {
		t.Fatalf("oldest kept line %q, want line 51", got.Log[0].Text)
	}
	last := got.Log[len(got.Log)-1].Text
	if len(last) > MaxLineBytes || !strings.HasSuffix(last, "…") {
		t.Fatalf("long line not truncated to %d bytes: %d", MaxLineBytes, len(last))
	}
}

func TestMarkInterrupted(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	o := s.Start(ctx, Spec{Kind: "backup.run", Target: "nightly", Steps: []StepSpec{{"archive", "Archive"}, {"verify", "Verify"}}})
	o.StepStart("archive", "")
	done := s.Start(ctx, Spec{Kind: "image.update", Target: "web"})
	done.Finish(nil)

	// A new process sees only what was written.
	s2 := New(database)
	n, err := s2.MarkInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("marked %d, want 1", n)
	}
	got, err := s2.Get(ctx, o.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || got.Error != RestartReason || got.FinishedAt == nil {
		t.Fatalf("got %+v", got)
	}
	if got.Steps[0].Status != StepFailed || got.Steps[1].Status != StepSkipped {
		t.Fatalf("steps %+v", got.Steps)
	}
	other, _ := s2.Get(ctx, done.ID())
	if other.Status != StatusSucceeded {
		t.Fatalf("finished operation changed to %s", other.Status)
	}
}

func TestPruneKeepsRecentAndActive(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	base := time.Now()
	s.now = func() time.Time { return base.Add(-8 * 24 * time.Hour) }
	old := s.Start(ctx, Spec{Kind: "image.update", Target: "old"})
	old.Finish(nil)
	oldActive := s.Start(ctx, Spec{Kind: "image.update", Target: "stuck"})
	s.now = func() time.Time { return base }
	fresh := s.Start(ctx, Spec{Kind: "image.update", Target: "new"})
	fresh.Finish(nil)

	n, err := s.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM operations WHERE id IN (?, ?)`, oldActive.ID(), fresh.ID()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("kept %d of the active and recent operations, want 2", count)
	}
}

func TestListFiltersAndLiveState(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	a := s.Start(ctx, Spec{Kind: "backup.run", Ref: "backup-run:1", Target: "a"})
	b := s.Start(ctx, Spec{Kind: "backup.restore", Ref: "backup-run:1", Target: "a"})
	c := s.Start(ctx, Spec{Kind: "image.update", Target: "web", GroupID: "g1"})
	b.Finish(nil)
	a.StepStart("archive", "Archive")
	a.StepProgress("archive", 0.25, "")

	list, err := s.List(ctx, Filter{Kind: "backup."})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("backup. matched %d", len(list))
	}
	active, _ := s.List(ctx, Filter{ActiveOnly: true})
	if len(active) != 2 {
		t.Fatalf("active %d, want 2", len(active))
	}
	for _, op := range active {
		if op.ID == a.ID() && (len(op.Steps) != 1 || op.Steps[0].Progress == nil || *op.Steps[0].Progress != 0.25) {
			t.Fatalf("live progress missing from list: %+v", op.Steps)
		}
	}
	g, _ := s.List(ctx, Filter{GroupID: "g1"})
	if len(g) != 1 || g[0].ID != c.ID() {
		t.Fatalf("group filter %+v", g)
	}
	byKind, _ := s.List(ctx, Filter{Kind: "backup_", Ref: "backup-run:1"})
	if len(byKind) != 0 {
		t.Fatalf("LIKE wildcard not escaped: %d", len(byKind))
	}
}

func TestCancelOnlyQueued(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	cancelled := false
	q := s.Start(ctx, Spec{Kind: "image.update", Target: "a", Queued: true})
	q.SetCancel(func() bool { cancelled = true; return true })
	r := s.Start(ctx, Spec{Kind: "image.update", Target: "b"})
	r.SetCancel(func() bool { return true })
	if err := s.Cancel(r.ID()); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("running op cancel: %v", err)
	}
	if got, _ := s.Get(ctx, q.ID()); !got.Cancellable {
		t.Fatal("queued operation with a cancel func not marked cancellable")
	}
	if got, _ := s.Get(ctx, r.ID()); got.Cancellable {
		t.Fatal("running operation marked cancellable by a queue cancel func")
	}
	if err := s.Cancel(q.ID()); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("cancel func not called")
	}
	got, _ := s.Get(ctx, q.ID())
	if got.Status != StatusCancelled {
		t.Fatalf("status %s", got.Status)
	}
	q.Begin()
	q.Logf("info", "late")
	got, _ = s.Get(ctx, q.ID())
	if got.Status != StatusCancelled || len(got.Log) != 0 {
		t.Fatalf("ended operation changed: %+v", got)
	}
}

func TestCancelRunningInsideWindow(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	steps := []StepSpec{{"pull", "Pull image"}, {"swap", "Replace container"}}
	tests := []struct {
		name       string
		finishErr  error
		wantStatus Status
	}{
		{"work stops with an error", ErrCancelled, StatusCancelled},
		{"work completed anyway", nil, StatusSucceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := s.Start(ctx, Spec{Kind: "image.update", Target: "web", Steps: steps})
			o.StepStart("pull", "")
			if err := s.Cancel(o.ID()); !errors.Is(err, ErrNotCancellable) {
				t.Fatalf("cancel before the window opened: %v", err)
			}
			wctx, stop := context.WithCancelCause(ctx)
			o.AllowCancel(stop)
			if got, _ := s.Get(ctx, o.ID()); !got.Cancellable || got.CancelRequested {
				t.Fatalf("window open: %+v", got)
			}
			if err := s.Cancel(o.ID()); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(context.Cause(wctx), ErrCancelled) {
				t.Fatalf("work context cause %v", context.Cause(wctx))
			}
			got, _ := s.Get(ctx, o.ID())
			if got.Status != StatusRunning || !got.CancelRequested || got.Cancellable {
				t.Fatalf("after cancel request: %+v", got)
			}
			if err := s.Cancel(o.ID()); !errors.Is(err, ErrNotCancellable) {
				t.Fatalf("second cancel: %v", err)
			}
			o.DisallowCancel()
			if tc.finishErr != nil {
				o.StepFail("pull", tc.finishErr)
			}
			o.Finish(tc.finishErr)
			got, _ = s.Get(ctx, o.ID())
			if got.Status != tc.wantStatus || got.Cancellable {
				t.Fatalf("status %s, want %s", got.Status, tc.wantStatus)
			}
			if tc.wantStatus == StatusCancelled {
				if got.Error != "cancelled during “Pull image”" {
					t.Fatalf("error %q", got.Error)
				}
				if got.Steps[0].Status != StepCancelled || got.Steps[1].Status != StepSkipped {
					t.Fatalf("steps %+v", got.Steps)
				}
			}
		})
	}
}

func TestCancelOutsideWindow(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	o := s.Start(ctx, Spec{Kind: "backup.restore", Target: "data"})
	_, stop := context.WithCancelCause(ctx)
	o.AllowCancel(stop)
	o.DisallowCancel()
	if err := s.Cancel(o.ID()); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel after the window closed: %v", err)
	}
	o.Finish(nil)
	if err := s.Cancel(o.ID()); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel of an ended operation: %v", err)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	s, _ := newTestService(t)
	ch, unsub := s.Subscribe()
	defer unsub()
	o := s.Start(context.Background(), Spec{Kind: "image.update", Target: "web"})
	o.Logf("info", "pulling")
	o.Finish(nil)
	var types []string
	for i := 0; i < 3; i++ {
		select {
		case ev := <-ch:
			types = append(types, ev.Type)
			if ev.Op.Log != nil {
				t.Fatal("event carries the log")
			}
		case <-time.After(time.Second):
			t.Fatalf("got only %v", types)
		}
	}
	if strings.Join(types, ",") != "update,log,update" {
		t.Fatalf("events %v", types)
	}
}

func TestMaskText(t *testing.T) {
	tests := []struct {
		name, in string
		secrets  []string
		leak     string
	}{
		{"registered", "auth failed for hunter2-secret", []string{"hunter2-secret"}, "hunter2-secret"},
		{"key value", "connect password=s3cr3tvalue host=x", nil, "s3cr3tvalue"},
		{"quoted", `secret_key: "AKIAzzzz/abc"`, nil, "AKIAzzzz"},
		{"url", "dial https://admin:pa55w0rd@nas.local/share", nil, "pa55w0rd"},
		{"bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.x.y", nil, "eyJhbGciOiJIUzI1NiJ9"},
		{"header", "X-Api-Key: abcdef123456", nil, "abcdef123456"},
		{"pem", "key -----BEGIN OPENSSH PRIVATE KEY-----\nAAAAB3Nza\n-----END OPENSSH PRIVATE KEY-----", nil, "AAAAB3Nza"},
		{"base64", "auth=eyJ1c2VybmFtZSI6InUifQ", []string{`{"username":"u"}`}, "eyJ1c2VybmFtZSI6InUifQ"},
		{"escaped", "GET /x?p=a%2Fb%3Dc", []string{"a/b=c"}, "a%2Fb%3Dc"},
		{"key line", "hook printed b3BlbnNzaC1rZXktdjEAAAAA", []string{"-----BEGIN KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END KEY-----"}, "b3BlbnNzaC1rZXktdjEAAAAA"},
		{"json escaped", `error: {"pw":"a\"quoted\"pw"}`, []string{`a"quoted"pw`}, `quoted\"pw`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var list []string
			for _, s := range tt.secrets {
				list = appendSecret(list, s)
			}
			got := MaskText(tt.in, list)
			if strings.Contains(got, tt.leak) {
				t.Fatalf("%q leaks %q", got, tt.leak)
			}
		})
	}
	if got := MaskText("pulled 3 layers", appendSecret(nil, "ab")); got != "pulled 3 layers" {
		t.Fatalf("short secret masked text: %q", got)
	}
}

func TestOpMasksStoredAndPublished(t *testing.T) {
	s, _ := newTestService(t)
	ch, unsub := s.Subscribe()
	defer unsub()
	const secret = "Sup3r-Secret-Value"
	o := s.Start(context.Background(), Spec{Kind: "backup.run", Target: "job " + secret, Secrets: []string{secret},
		Steps: []StepSpec{{"upload", "Upload"}}})
	o.StepStart("upload", "")
	o.StepProgress("upload", 0.1, "to smb://u:"+secret+"@nas")
	o.Logf("error", "login with %s failed", secret)
	o.StepFail("upload", errors.New("bad password "+secret))
	o.Finish(errors.New("upload: " + secret))

	got, _ := s.Get(context.Background(), o.ID())
	assertNoSecret(t, got, secret)
	for {
		select {
		case ev := <-ch:
			assertNoSecret(t, &ev.Op, secret)
			if ev.Line != nil && strings.Contains(ev.Line.Text, secret) {
				t.Fatalf("published line leaks: %q", ev.Line.Text)
			}
		default:
			return
		}
	}
}

func assertNoSecret(t *testing.T, op *Operation, secret string) {
	t.Helper()
	check := func(where, s string) {
		if strings.Contains(s, secret) {
			t.Fatalf("%s leaks the secret: %q", where, s)
		}
	}
	check("target", op.Target)
	check("error", op.Error)
	for _, st := range op.Steps {
		check("step "+st.Key, st.Detail)
	}
	for _, l := range op.Log {
		check("log", l.Text)
	}
}

package selfupdate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

type releaseServer struct {
	files map[string][]byte // path below the base, e.g. v0.6.1/checksums.txt
}

func (s *releaseServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, ok := s.files[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

type installFixture struct {
	t       *testing.T
	dir     string
	srv     *releaseServer
	key     minisign.PrivateKey
	inst    *Installer
	locks   *stacklock.Locks
	notices *notices.Service
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := releasesig.New(pub.String())
	if err != nil {
		t.Fatal(err)
	}
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &installFixture{t: t, dir: filepath.Join(t.TempDir(), "update"), srv: &releaseServer{files: map[string][]byte{}}, key: priv,
		locks: stacklock.New(), notices: notices.NewService(database)}
	hs := httptest.NewServer(f.srv)
	t.Cleanup(hs.Close)
	f.inst, err = NewInstaller(InstallerConfig{
		Dir:     f.dir,
		Current: "v0.6.0",
		Locks:   f.locks,
		Targets: func(context.Context) ([]stacklock.Target, error) {
			return []stacklock.Target{stacklock.Host("local")}, nil
		},
		Notices:          f.notices,
		UpdaterInstalled: func() bool { return true },
		BaseURL:          hs.URL + "/",
		Verifier:         v,
		LockWait:         200 * time.Millisecond,
		ApplyWait:        2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// publish puts a release signed for signTag under v0.6.1.
func (f *installFixture) publish(signTag string, archive []byte) {
	const tag = "v0.6.1"
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + Asset() + "\n")
	f.srv.files[tag+"/"+releasesig.ChecksumsFile] = checksums
	f.srv.files[tag+"/"+releasesig.SignatureFile] = minisign.SignWithComments(f.key, checksums, releasesig.TrustedComment(signTag), "")
	f.srv.files[tag+"/"+Asset()] = archive
}

func (f *installFixture) wait(phases ...string) InstallState {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := f.inst.State()
		for _, p := range phases {
			if st.Phase == p {
				return st
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("state %+v, want one of %v", st, phases)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// updater answers a request with res, like the root updater does when it
// refuses or rolls back without stopping the server.
func (f *installFixture) updater(res rootupdate.Result) <-chan rootupdate.Request {
	got := make(chan rootupdate.Request, 1)
	go func() {
		p := filepath.Join(f.dir, rootupdate.RequestFile)
		for i := 0; i < 500; i++ {
			if data, err := os.ReadFile(p); err == nil {
				var r rootupdate.Request
				_ = json.Unmarshal(data, &r)
				_ = os.Remove(p)
				b, _ := json.Marshal(res)
				_ = os.WriteFile(filepath.Join(f.dir, rootupdate.ResultFile), b, 0o644)
				got <- r
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(got)
	}()
	return got
}

func TestInstallStartRefuses(t *testing.T) {
	f := newInstallFixture(t)
	tests := []struct {
		version string
		want    error
	}{
		{"v0.5.3", ErrNotNewer},
		{"v0.6.0", ErrNotNewer},
		{"v0.6.0-rc.2", ErrNotNewer},
		{"latest", ErrInvalidVersion},
		{"v0.6.1/../../x", ErrInvalidVersion},
	}
	for _, tt := range tests {
		if err := f.inst.Start(context.Background(), tt.version); !errors.Is(err, tt.want) {
			t.Errorf("Start(%q) = %v, want %v", tt.version, err, tt.want)
		}
	}
	f.inst.cfg.UpdaterInstalled = func() bool { return false }
	if err := f.inst.Start(context.Background(), "v0.6.1"); !errors.Is(err, ErrUpdaterMissing) {
		t.Errorf("without the updater: %v", err)
	}
}

func TestInstallWritesRequestAfterChecks(t *testing.T) {
	f := newInstallFixture(t)
	f.publish("v0.6.1", []byte("archive"))
	got := f.updater(rootupdate.Result{Status: rootupdate.StatusRefused, From: "v0.6.0", To: "v0.6.1", Reason: "test refusal"})
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	if err := f.inst.Start(context.Background(), "v0.6.1"); !errors.Is(err, ErrInstallRunning) {
		t.Errorf("second start: %v", err)
	}
	req := <-got
	if req.Version != "v0.6.1" {
		t.Fatalf("request %+v", req)
	}
	st := f.wait(PhaseFailed)
	if !strings.Contains(st.Error, "test refusal") {
		t.Errorf("state %+v", st)
	}
	open, err := f.notices.Open(context.Background())
	if err != nil || len(open) != 1 || open[0].Key != ResultNoticeKey || !strings.Contains(open[0].Body, "test refusal") {
		t.Fatalf("notices %+v, %v", open, err)
	}
	// The stacks are free again once the updater refused.
	release, err := f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatalf("stacks still held: %v", err)
	}
	release()
	if _, err := os.Stat(filepath.Join(f.dir, Asset())); !os.IsNotExist(err) {
		t.Error("downloaded archive kept after the result")
	}
}

func TestInstallRefusesBadDownload(t *testing.T) {
	tests := []struct {
		name    string
		publish func(f *installFixture)
	}{
		{"missing release", func(f *installFixture) {}},
		{"signature for another tag", func(f *installFixture) { f.publish("v0.6.2", []byte("archive")) }},
		{"tampered archive", func(f *installFixture) {
			f.publish("v0.6.1", []byte("archive"))
			f.srv.files["v0.6.1/"+Asset()] = []byte("evil")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstallFixture(t)
			tt.publish(f)
			if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
				t.Fatal(err)
			}
			f.wait(PhaseFailed)
			if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
				t.Fatal("request written for a bad download")
			}
			if _, err := os.Stat(filepath.Join(f.dir, Asset())); !os.IsNotExist(err) {
				t.Fatal("bad archive kept")
			}
		})
	}
}

func TestInstallWaitsForBusyStacks(t *testing.T) {
	f := newInstallFixture(t)
	f.publish("v0.6.1", []byte("archive"))
	release, err := f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	st := f.wait(PhaseFailed)
	release()
	if st.Error != errStacksStillBusy.Error() || strings.Contains(st.Error, "web") {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
		t.Fatal("request written while a stack was busy")
	}

	// A stack that frees up within the wait lets the install go on.
	f.inst.cfg.LockWait = 5 * time.Second
	release, _ = f.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpDeploy)
	got := f.updater(rootupdate.Result{Status: rootupdate.StatusRefused, To: "v0.6.1", Reason: "x"})
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	f.wait(PhaseWaiting)
	if _, err := f.locks.TryLock(stacklock.HostKey("local", "db"), stacklock.OpDeploy); !errors.Is(err, stacklock.ErrBusy) {
		t.Fatalf("new stack operation allowed while the install waits: %v", err)
	}
	release()
	if req := <-got; req.Version != "v0.6.1" {
		t.Fatalf("request %+v", req)
	}
}

func TestInstallNoAnswerFromUpdater(t *testing.T) {
	f := newInstallFixture(t)
	f.inst.cfg.ApplyWait = 100 * time.Millisecond
	f.publish("v0.6.1", []byte("archive"))
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	st := f.wait(PhaseFailed)
	if st.Error != errUpdaterNoAnswer.Error() {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
		t.Fatal("stale request left for the updater")
	}
}

func TestCheckResultNotices(t *testing.T) {
	tests := []struct {
		status   rootupdate.Status
		notice   bool
		severity notices.Severity
		keep     bool
	}{
		{rootupdate.StatusUpdated, false, "", false},
		{rootupdate.StatusRefused, true, notices.SevWarning, false},
		{rootupdate.StatusRolledBack, true, notices.SevWarning, false},
		{rootupdate.StatusFailed, true, notices.SevCritical, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			f := newInstallFixture(t)
			// A notice from an earlier update goes away with a new result.
			_ = f.notices.Raise(context.Background(), notices.RaiseInput{Key: ResultNoticeKey, Audience: notices.Admins(), Severity: notices.SevWarning,
				Title: i18n.Msg("notice.update_refused.title", "old")})
			b, _ := json.Marshal(rootupdate.Result{Status: tt.status, From: "v0.6.0", To: "v0.6.1", Reason: "why"})
			if err := os.WriteFile(filepath.Join(f.dir, rootupdate.ResultFile), b, 0o644); err != nil {
				t.Fatal(err)
			}
			res := f.inst.CheckResult(context.Background())
			if res.Status != tt.status {
				t.Fatalf("result %+v", res)
			}
			open, _ := f.notices.Open(context.Background())
			if tt.notice != (len(open) == 1) || tt.notice && (open[0].Severity != tt.severity || strings.Contains(open[0].Title, "old")) {
				t.Fatalf("notices %+v", open)
			}
			_, err := os.Stat(filepath.Join(f.dir, rootupdate.ResultFile))
			if tt.keep != (err == nil) {
				t.Fatalf("result file kept = %v, want %v", err == nil, tt.keep)
			}
		})
	}
}

func TestAwaitResultWaitsForPending(t *testing.T) {
	f := newInstallFixture(t)
	setPoll(t, 5*time.Millisecond)
	pending := filepath.Join(f.dir, rootupdate.PendingFile)
	if err := os.WriteFile(pending, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { f.inst.AwaitResult(context.Background()); close(done) }()
	time.Sleep(30 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("returned while the update was pending")
	default:
	}
	if !f.inst.UpdatePending() {
		t.Fatal("UpdatePending false with the marker in place")
	}
	b, _ := json.Marshal(rootupdate.Result{Status: rootupdate.StatusRolledBack, From: "v0.6.0", To: "v0.6.1", Reason: "health"})
	_ = os.WriteFile(filepath.Join(f.dir, rootupdate.ResultFile), b, 0o644)
	_ = os.Remove(pending)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the marker went")
	}
	if open, _ := f.notices.Open(context.Background()); len(open) != 1 {
		t.Fatalf("notices %+v", open)
	}
}

func setPoll(t *testing.T, d time.Duration) {
	old := pendingPoll
	pendingPoll = d
	t.Cleanup(func() { pendingPoll = old })
}

func TestInstallRefusedWhileUpdatePending(t *testing.T) {
	f := newInstallFixture(t)
	_ = os.WriteFile(filepath.Join(f.dir, rootupdate.PendingFile), []byte("{}"), 0o644)
	if err := f.inst.Start(context.Background(), "v0.6.1"); !errors.Is(err, ErrInstallRunning) {
		t.Fatalf("Start while an update is pending: %v", err)
	}
}

func TestInstallIgnoresResultOfAnotherUpdate(t *testing.T) {
	f := newInstallFixture(t)
	f.inst.cfg.ApplyWait = 300 * time.Millisecond
	f.publish("v0.6.1", []byte("archive"))
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	f.wait(PhaseApplying)
	b, _ := json.Marshal(rootupdate.Result{Status: rootupdate.StatusUpdated, To: "v0.6.0"})
	_ = os.WriteFile(filepath.Join(f.dir, rootupdate.ResultFile), b, 0o644)
	st := f.wait(PhaseFailed)
	if st.Error != errUpdaterNoAnswer.Error() {
		t.Fatalf("state %+v: another update's result was taken for this one", st)
	}
}

func TestInstallFailsClosedWithoutHostList(t *testing.T) {
	f := newInstallFixture(t)
	f.inst.cfg.Targets = func(context.Context) ([]stacklock.Target, error) { return nil, errors.New("db down") }
	f.publish("v0.6.1", []byte("archive"))
	if err := f.inst.Start(context.Background(), "v0.6.1"); err != nil {
		t.Fatal(err)
	}
	st := f.wait(PhaseFailed)
	if !strings.Contains(st.Error, "db down") {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, rootupdate.RequestFile)); !os.IsNotExist(err) {
		t.Fatal("request written without holding every host")
	}
	if _, err := os.Stat(filepath.Join(f.dir, Asset())); !os.IsNotExist(err) {
		t.Fatal("downloaded release kept after the install ended")
	}
}

func TestAwaitResultRaisesStuckNotice(t *testing.T) {
	f := newInstallFixture(t)
	setPoll(t, 5*time.Millisecond)
	oldAfter := unconfirmedAfter
	unconfirmedAfter = 20 * time.Millisecond
	t.Cleanup(func() { unconfirmedAfter = oldAfter })
	pending := filepath.Join(f.dir, rootupdate.PendingFile)
	_ = os.WriteFile(pending, []byte("{}"), 0o644)
	done := make(chan struct{})
	go func() { f.inst.AwaitResult(context.Background()); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		open, _ := f.notices.Open(context.Background())
		if len(open) == 1 && open[0].Key == UnconfirmedNoticeKey {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stuck notice: %+v", open)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = os.Remove(pending)
	<-done
	if open, _ := f.notices.Open(context.Background()); len(open) != 0 {
		t.Fatalf("stuck notice still open: %+v", open)
	}
}

func TestAwaitResultResolvesStuckNoticeOfEarlierProcess(t *testing.T) {
	f := newInstallFixture(t)
	_ = f.notices.Raise(context.Background(), notices.RaiseInput{Key: UnconfirmedNoticeKey, Audience: notices.Admins(),
		Severity: notices.SevCritical, Blocking: true, Title: i18n.Msg("notice.update_unconfirmed.title")})
	f.inst.AwaitResult(context.Background())
	if open, _ := f.notices.Open(context.Background()); len(open) != 0 {
		t.Fatalf("stuck notice of an earlier process still open: %+v", open)
	}
}

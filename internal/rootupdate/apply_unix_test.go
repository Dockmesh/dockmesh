//go:build unix

package rootupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

func init() {
	// rootOnlyDir refuses group-writable test directories.
	syscall.Umask(0o022)
	rootOwners = append(rootOwners, uint32(os.Getuid()))
}

const (
	testAsset = "dockmesh_linux_amd64.tar.gz"
	oldUnit   = "old unit"
)

// fakeService models the service by the content of the installed binary:
// "bin <version>" runs healthy as that version, anything else never does.
type fakeService struct {
	binary   string
	failAt   map[string]error
	calls    []string
	running  bool
	unit     []byte
	data     string // current data; Snapshot copies it, RestoreSnapshot puts it back
	snapshot string
	healthy  func(version string) bool
}

func (f *fakeService) fail(step string) error {
	f.calls = append(f.calls, step)
	return f.failAt[step]
}

func (f *fakeService) Stop(context.Context) error {
	if err := f.fail("stop"); err != nil {
		return err
	}
	f.running = false
	return nil
}

func (f *fakeService) Start(context.Context) error {
	if err := f.fail("start"); err != nil {
		return err
	}
	f.running = true
	// A started new version migrates the data.
	if v := f.version(); v != "" {
		f.data = "data of " + v
	}
	return nil
}

func (f *fakeService) ReadUnit() ([]byte, error) {
	if err := f.fail("read-unit"); err != nil {
		return nil, err
	}
	return bytes.Clone(f.unit), nil
}

func (f *fakeService) RestoreUnit(data []byte) error {
	if err := f.fail("restore-unit"); err != nil {
		return err
	}
	f.unit = bytes.Clone(data)
	return nil
}

func (f *fakeService) WriteUnit(_ context.Context, binary string) error {
	if err := f.fail("write-unit"); err != nil {
		return err
	}
	f.unit = []byte("unit for " + binary)
	return nil
}

func (f *fakeService) DaemonReload(context.Context) error { return f.fail("daemon-reload") }

func (f *fakeService) Snapshot(context.Context) error {
	if err := f.fail("snapshot"); err != nil {
		return err
	}
	f.snapshot = f.data
	return nil
}

func (f *fakeService) RestoreSnapshot(context.Context) error {
	if err := f.fail("restore-snapshot"); err != nil {
		return err
	}
	f.data = f.snapshot
	return nil
}

func (f *fakeService) version() string {
	b, err := os.ReadFile(f.binary)
	if err != nil {
		return ""
	}
	v, ok := strings.CutPrefix(string(b), "bin ")
	if !ok {
		return ""
	}
	return v
}

func (f *fakeService) Healthy(_ context.Context, version string) bool {
	if f.healthy != nil {
		return f.healthy(version)
	}
	return f.running && f.version() == version
}

type fixture struct {
	t        *testing.T
	reqDir   string
	stateDir string
	binDir   string
	shareDir string
	key      minisign.PrivateKey
	verifier *releasesig.Verifier
	svc      *fakeService
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	f := &fixture{
		t:        t,
		reqDir:   filepath.Join(base, "data", "update"),
		stateDir: filepath.Join(base, "state"),
		binDir:   filepath.Join(base, "bin"),
		shareDir: filepath.Join(base, "share", "bin"),
	}
	for _, d := range []string{f.reqDir, f.binDir, f.shareDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.key = priv
	if f.verifier, err = releasesig.New(pub.String()); err != nil {
		t.Fatal(err)
	}
	f.svc = &fakeService{binary: filepath.Join(f.binDir, "dockmesh"), unit: []byte(oldUnit), running: true, data: "data of v0.6.0"}
	f.write(filepath.Join(f.binDir, "dockmesh"), "bin v0.6.0")
	f.write(filepath.Join(f.binDir, "dmctl"), "dmctl v0.6.0")
	return f
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(path string) string {
	f.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func (f *fixture) config() Config {
	return Config{
		Installed:      "v0.6.0",
		RequestDir:     f.reqDir,
		ServiceUID:     os.Getuid(),
		StateDir:       f.stateDir,
		Asset:          testAsset,
		Verifier:       f.verifier,
		Service:        f.svc,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthTimeout:  50 * time.Millisecond,
		HealthInterval: 5 * time.Millisecond,
		Files: []File{
			{Name: "dockmesh", Dest: filepath.Join(f.binDir, "dockmesh"), KeepPrev: true},
			{Name: "dmctl", Dest: filepath.Join(f.binDir, "dmctl"), Optional: true},
			{Name: "dockmesh-agent-linux-amd64", Dest: filepath.Join(f.shareDir, "dockmesh-agent-linux-amd64"), Optional: true},
			{Name: "install-agent.sh", Dest: filepath.Join(f.shareDir, "missing", "install-agent.sh"), Optional: true},
		},
	}
}

type entry struct {
	name    string
	content string
	typ     byte
}

func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: typ}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.content))
		} else {
			hdr.Linkname = e.content
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodTarball(t *testing.T, version string) []byte {
	return tarball(t,
		entry{name: "dockmesh", content: "bin " + version},
		entry{name: "dockmesh-agent-linux-amd64", content: "agent " + version},
		entry{name: "dmctl", content: "dmctl " + version},
		entry{name: "LICENSE", content: "license"},
	)
}

// release writes a signed release for tag into the request dir and a
// request for version.
func (f *fixture) release(tag, version string, tgz []byte) {
	f.t.Helper()
	sum := sha256.Sum256(tgz)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + testAsset + "\n")
	sig := minisign.SignWithComments(f.key, checksums, releasesig.TrustedComment(tag), "")
	f.write(filepath.Join(f.reqDir, releasesig.ChecksumsFile), string(checksums))
	f.write(filepath.Join(f.reqDir, releasesig.SignatureFile), string(sig))
	f.write(filepath.Join(f.reqDir, testAsset), string(tgz))
	f.request(version)
}

func (f *fixture) request(version string) {
	f.t.Helper()
	b, _ := json.Marshal(Request{Version: version})
	f.write(filepath.Join(f.reqDir, RequestFile), string(b))
}

func (f *fixture) apply() Result {
	f.t.Helper()
	res, err := Apply(context.Background(), f.config())
	if err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
	return res
}

// assertUntouched checks the old version runs as before the run.
func (f *fixture) assertOldRunning(stopped bool) {
	f.t.Helper()
	if got := f.read(filepath.Join(f.binDir, "dockmesh")); got != "bin v0.6.0" {
		f.t.Errorf("binary = %q, want the old one", got)
	}
	if got := f.read(filepath.Join(f.binDir, "dmctl")); got != "dmctl v0.6.0" {
		f.t.Errorf("dmctl = %q, want the old one", got)
	}
	if _, err := os.Lstat(filepath.Join(f.shareDir, "dockmesh-agent-linux-amd64")); !errors.Is(err, os.ErrNotExist) {
		f.t.Errorf("agent binary created by a failed update: %v", err)
	}
	if string(f.svc.unit) != oldUnit {
		f.t.Errorf("unit = %q, want the old one", f.svc.unit)
	}
	if f.svc.data != "data of v0.6.0" {
		f.t.Errorf("data = %q, want the old data", f.svc.data)
	}
	if !f.svc.running {
		f.t.Error("service not running")
	}
	stoppedNow := false
	for _, c := range f.svc.calls {
		if c == "stop" {
			stoppedNow = true
		}
	}
	if stoppedNow != stopped {
		f.t.Errorf("service stopped = %v, want %v (calls %v)", stoppedNow, stopped, f.svc.calls)
	}
	for _, name := range []string{PendingFile, RequestFile} {
		if _, err := os.Lstat(filepath.Join(f.reqDir, name)); !errors.Is(err, os.ErrNotExist) {
			f.t.Errorf("%s left behind: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.stateDir, journalName)); !errors.Is(err, os.ErrNotExist) {
		f.t.Errorf("journal left behind: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(f.binDir, ".*"))
	if len(left) > 0 {
		f.t.Errorf("staged files left behind: %v", left)
	}
}

func (f *fixture) storedResult() Result {
	f.t.Helper()
	var r Result
	if err := json.Unmarshal([]byte(f.read(filepath.Join(f.reqDir, ResultFile))), &r); err != nil {
		f.t.Fatalf("result file: %v", err)
	}
	return r
}

func TestApplyNoRequest(t *testing.T) {
	f := newFixture(t)
	res := f.apply()
	if res.Status != "" {
		t.Fatalf("status %q without a request", res.Status)
	}
	if _, err := os.Lstat(filepath.Join(f.reqDir, ResultFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("result written without a request")
	}
}

func TestApplyUpdates(t *testing.T) {
	f := newFixture(t)
	f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
	res := f.apply()
	if res.Status != StatusUpdated || res.From != "v0.6.0" || res.To != "v0.6.1" {
		t.Fatalf("result %+v", res)
	}
	if got := f.storedResult(); got.Status != StatusUpdated {
		t.Fatalf("stored result %+v", got)
	}
	checks := map[string]string{
		filepath.Join(f.binDir, "dockmesh"):                     "bin v0.6.1",
		filepath.Join(f.binDir, "dockmesh.prev"):                "bin v0.6.0",
		filepath.Join(f.binDir, "dmctl"):                        "dmctl v0.6.1",
		filepath.Join(f.shareDir, "dockmesh-agent-linux-amd64"): "agent v0.6.1",
	}
	for path, want := range checks {
		if got := f.read(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if string(f.svc.unit) != "unit for "+filepath.Join(f.binDir, "dockmesh") {
		t.Errorf("unit = %q", f.svc.unit)
	}
	if f.svc.snapshot != "data of v0.6.0" {
		t.Errorf("snapshot = %q, want the data of the stopped old version", f.svc.snapshot)
	}
	fi, err := os.Stat(filepath.Join(f.binDir, "dockmesh"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("installed binary mode: %v %v", fi, err)
	}
	for _, p := range []string{filepath.Join(f.reqDir, PendingFile), filepath.Join(f.stateDir, journalName), filepath.Join(f.stateDir, testAsset)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind", p)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(f.stateDir, "backup-*")); len(left) > 0 {
		t.Errorf("backups left behind: %v", left)
	}
	want := []string{"stop", "snapshot", "read-unit", "write-unit", "daemon-reload", "start"}
	if strings.Join(f.svc.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls %v, want %v", f.svc.calls, want)
	}
	fi, err = os.Stat(f.stateDir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode: %v %v", fi, err)
	}
}

func TestApplyRefuses(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
		step  string
	}{
		{"request symlink", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			target := filepath.Join(f.reqDir, "elsewhere")
			f.write(target, `{"version":"v0.6.1"}`)
			_ = os.Remove(filepath.Join(f.reqDir, RequestFile))
			if err := os.Symlink(target, filepath.Join(f.reqDir, RequestFile)); err != nil {
				t.Fatal(err)
			}
		}, StepRequest},
		{"request fifo", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			_ = os.Remove(filepath.Join(f.reqDir, RequestFile))
			if err := syscall.Mkfifo(filepath.Join(f.reqDir, RequestFile), 0o644); err != nil {
				t.Fatal(err)
			}
		}, StepRequest},
		{"request directory", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			_ = os.Remove(filepath.Join(f.reqDir, RequestFile))
			if err := os.MkdirAll(filepath.Join(f.reqDir, RequestFile, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, StepRequest},
		{"request hardlink", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			if err := os.Link(filepath.Join(f.reqDir, RequestFile), filepath.Join(f.reqDir, "other")); err != nil {
				t.Fatal(err)
			}
		}, StepRequest},
		{"request too large", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			f.write(filepath.Join(f.reqDir, RequestFile), `{"version":"v0.6.1","x":"`+strings.Repeat("a", MaxRequestSize)+`"}`)
		}, StepRequest},
		{"request not json", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			f.write(filepath.Join(f.reqDir, RequestFile), "v0.6.1")
		}, StepRequest},
		{"not a tag", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			f.request("../../v0.6.1")
		}, StepRequest},
		{"downgrade", func(f *fixture) {
			f.release("v0.5.3", "v0.5.3", goodTarball(t, "v0.5.3"))
		}, StepRequest},
		{"same version", func(f *fixture) {
			f.release("v0.6.0", "v0.6.0", goodTarball(t, "v0.6.0"))
		}, StepRequest},
		{"release candidate of the installed release", func(f *fixture) {
			f.release("v0.6.0-rc.3", "v0.6.0-rc.3", goodTarball(t, "v0.6.0-rc.3"))
		}, StepRequest},
		{"checksums missing", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			_ = os.Remove(filepath.Join(f.reqDir, releasesig.ChecksumsFile))
		}, StepVerify},
		{"signature of another tag", func(f *fixture) {
			f.release("v0.6.2", "v0.6.2", goodTarball(t, "v0.6.1"))
			f.request("v0.6.1")
		}, StepVerify},
		{"signature by another key", func(f *fixture) {
			_, other, _ := minisign.GenerateKey(rand.Reader)
			f.key = other
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
		}, StepVerify},
		{"tampered checksums", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			p := filepath.Join(f.reqDir, releasesig.ChecksumsFile)
			f.write(p, strings.Replace(f.read(p), testAsset, testAsset+" ", 1))
		}, StepVerify},
		{"tampered tarball", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			f.write(filepath.Join(f.reqDir, testAsset), string(goodTarball(t, "v6.6.6")))
		}, StepVerify},
		{"tarball symlink", func(f *fixture) {
			tgz := goodTarball(t, "v0.6.1")
			f.release("v0.6.1", "v0.6.1", tgz)
			p := filepath.Join(f.reqDir, testAsset)
			f.write(p+".real", string(tgz))
			_ = os.Remove(p)
			if err := os.Symlink(p+".real", p); err != nil {
				t.Fatal(err)
			}
		}, StepVerify},
		{"binary missing from tarball", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", tarball(t, entry{name: "dmctl", content: "dmctl v0.6.1"}))
		}, StepStage},
		{"binary is a symlink in the tarball", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", tarball(t, entry{name: "dockmesh", content: "/bin/sh", typ: tar.TypeSymlink}))
		}, StepStage},
		{"binary twice in the tarball", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", tarball(t,
				entry{name: "dockmesh", content: "bin v0.6.1"},
				entry{name: "./dockmesh", content: "evil"}))
		}, StepStage},
		{"not a gzip", func(f *fixture) {
			f.release("v0.6.1", "v0.6.1", []byte("plain"))
		}, StepStage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.setup(f)
			res := f.apply()
			if res.Status != StatusRefused || res.Step != tt.step {
				t.Fatalf("result %+v, want refused at %s", res, tt.step)
			}
			if got := f.storedResult(); got.Status != StatusRefused {
				t.Errorf("stored result %+v", got)
			}
			f.assertOldRunning(false)
		})
	}
}

func TestApplyInstalledDevBuild(t *testing.T) {
	f := newFixture(t)
	f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
	cfg := f.config()
	cfg.Installed = "dev"
	res, err := Apply(context.Background(), cfg)
	if err != nil || res.Status != StatusRefused {
		t.Fatalf("result %+v, %v", res, err)
	}
	f.assertOldRunning(false)
}

func TestApplyRequestDirOfAnotherAccount(t *testing.T) {
	f := newFixture(t)
	f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
	cfg := f.config()
	cfg.ServiceUID = os.Getuid() + 1
	if _, err := Apply(context.Background(), cfg); err == nil {
		t.Fatal("request dir of another account accepted")
	}
	f.assertOldRunningNoRequestCheck()
}

func (f *fixture) assertOldRunningNoRequestCheck() {
	f.t.Helper()
	if got := f.read(filepath.Join(f.binDir, "dockmesh")); got != "bin v0.6.0" {
		f.t.Errorf("binary = %q", got)
	}
	if len(f.svc.calls) > 0 {
		f.t.Errorf("service touched: %v", f.svc.calls)
	}
}

func TestApplyRollsBack(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		failAt string
		tgz    func(t *testing.T) []byte
		prep   func(f *fixture)
		step   string
	}{
		{name: "stop fails", failAt: "stop", step: StepStop},
		{name: "snapshot fails", failAt: "snapshot", step: StepSnapshot},
		{name: "install fails", step: StepInstall, prep: func(f *fixture) {
			if err := os.MkdirAll(filepath.Join(f.binDir, "dockmesh.prev", "x"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}},
		{name: "reading the unit fails", failAt: "read-unit", step: StepUnit},
		{name: "writing the unit fails", failAt: "write-unit", step: StepUnit},
		{name: "start fails", failAt: "start-once", step: StepStart},
		{name: "bad binary never gets healthy", step: StepHealth, tgz: func(t *testing.T) []byte {
			return tarball(t, entry{name: "dockmesh", content: "crashes"}, entry{name: "dmctl", content: "dmctl v0.6.1"},
				entry{name: "dockmesh-agent-linux-amd64", content: "agent v0.6.1"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tgz := goodTarball(t, "v0.6.1")
			if tt.tgz != nil {
				tgz = tt.tgz(t)
			}
			f.release("v0.6.1", "v0.6.1", tgz)
			if tt.prep != nil {
				tt.prep(f)
			}
			f.svc.failAt = map[string]error{}
			if tt.failAt == "start-once" {
				f.svc.healthy = nil
				starts := 0
				f.svc.failAt = nil
				svc := &startOnceFails{fakeService: f.svc, starts: &starts}
				cfg := f.config()
				cfg.Service = svc
				res, err := Apply(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				checkRolledBack(t, f, res, tt.step)
				return
			}
			if tt.failAt != "" {
				f.svc.failAt[tt.failAt] = boom
			}
			res := f.apply()
			checkRolledBack(t, f, res, tt.step)
		})
	}
}

func checkRolledBack(t *testing.T, f *fixture, res Result, step string) {
	t.Helper()
	if res.Status != StatusRolledBack || res.Step != step {
		t.Fatalf("result %+v, want rolled back at %s", res, step)
	}
	if got := f.storedResult(); got.Status != StatusRolledBack || got.Reason == "" {
		t.Errorf("stored result %+v", got)
	}
	if step == StepInstall {
		// The broken .prev blocks the backup, so nothing was swapped.
		f.assertOldRunning(true)
		return
	}
	f.assertOldRunning(true)
}

// startOnceFails fails the first start (the new version) only.
type startOnceFails struct {
	*fakeService
	starts *int
}

func (s *startOnceFails) Start(ctx context.Context) error {
	*s.starts++
	if *s.starts == 1 {
		s.calls = append(s.calls, "start")
		return errors.New("start failed")
	}
	return s.fakeService.Start(ctx)
}

func TestApplyRollbackFailureIsRetried(t *testing.T) {
	f := newFixture(t)
	f.release("v0.6.1", "v0.6.1", tarball(t, entry{name: "dockmesh", content: "crashes"}))
	f.svc.failAt = map[string]error{"restore-snapshot": errors.New("disk full")}
	res := f.apply()
	if res.Status != StatusFailed {
		t.Fatalf("result %+v, want failed", res)
	}
	if _, err := os.Lstat(filepath.Join(f.stateDir, journalName)); err != nil {
		t.Fatalf("journal gone after a failed rollback: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.reqDir, PendingFile)); err != nil {
		t.Fatalf("pending marker gone after a failed rollback: %v", err)
	}
	// The next run (e.g. at boot) finishes the rollback.
	f.svc.failAt = nil
	f.svc.calls = nil
	res = f.apply()
	if res.Status != StatusRolledBack || res.Step != StepRecover {
		t.Fatalf("second run %+v", res)
	}
	f.assertOldRunning(true)
}

// TestApplyRecoversAfterCrash covers a run that died between the swap and
// the confirmation: the next run keeps a healthy new version and rolls
// back one that does not answer.
func TestApplyRecoversAfterCrash(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		name := "new version unhealthy"
		if healthy {
			name = "new version healthy"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.release("v0.6.1", "v0.6.1", goodTarball(t, "v0.6.1"))
			cfg := f.config()
			if err := cfg.defaults(); err != nil {
				t.Fatal(err)
			}
			if err := ensureStateDir(cfg.StateDir); err != nil {
				t.Fatal(err)
			}
			req, err := openRequestDir(cfg.RequestDir, cfg.ServiceUID)
			if err != nil {
				t.Fatal(err)
			}
			u := &updater{cfg: cfg, req: req}
			to, err := u.readRequest()
			if err != nil {
				t.Fatal(err)
			}
			files, err := u.stage(to)
			if err != nil {
				t.Fatal(err)
			}
			j := &journal{From: "v0.6.0", To: to}
			_ = u.saveJournal(j)
			_ = u.writeJSON(PendingFile, Pending{From: "v0.6.0", To: to})
			_ = f.svc.Stop(context.Background())
			_ = f.svc.Snapshot(context.Background())
			j.Snapshot = true
			for i, s := range files {
				if err := u.install(j, i, s); err != nil {
					t.Fatal(err)
				}
			}
			j.Unit, j.UnitSaved = []byte(oldUnit), true
			_ = u.saveJournal(j)
			_ = f.svc.WriteUnit(context.Background(), cfg.Files[0].Dest)
			if healthy {
				_ = f.svc.Start(context.Background())
			}
			req.Close()
			// Crash here: no confirmation, no result.

			res := f.apply()
			if healthy {
				if res.Status != StatusUpdated || res.Step != StepRecover {
					t.Fatalf("result %+v", res)
				}
				if got := f.read(cfg.Files[0].Dest); got != "bin v0.6.1" {
					t.Fatalf("binary %q", got)
				}
				if _, err := os.Lstat(filepath.Join(f.reqDir, PendingFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("pending marker left after confirmation")
				}
				return
			}
			if res.Status != StatusRolledBack || res.Step != StepRecover {
				t.Fatalf("result %+v", res)
			}
			f.assertOldRunning(true)
		})
	}
}

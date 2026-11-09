package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/dockmesh/dockmesh/internal/notices"
)

// fakeDocker records calls in order and starts a fake admin API when the
// new container starts.
type fakeDocker struct {
	mu          sync.Mutex
	calls       []string
	exists      map[string]bool
	autosave    []byte
	failCreate  bool
	failStartID string
	onStart     func(id string)
}

func (f *fakeDocker) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists[id] {
		return container.InspectResponse{}, cerrdefs.ErrNotFound
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id}}, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *dnetwork.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.record("create " + name)
	if f.failCreate {
		return container.CreateResponse{}, errors.New("create refused")
	}
	return container.CreateResponse{ID: "new"}, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	f.record("start " + id)
	if id == f.failStartID {
		return errors.New("start refused")
	}
	if f.onStart != nil {
		f.onStart(id)
	}
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	f.record("stop " + id)
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.record("remove " + id)
	return nil
}

func (f *fakeDocker) ContainerRename(_ context.Context, id, name string) error {
	f.record("rename " + id + " " + name)
	return nil
}

func (f *fakeDocker) CopyToContainer(_ context.Context, id, dst string, content io.Reader, _ container.CopyToContainerOptions) error {
	b, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	if dst == "/config/caddy" && !bytes.Equal(b, f.autosave) {
		return errors.New("restored something other than the saved config")
	}
	f.record("copy " + id + " " + dst)
	return nil
}

func (f *fakeDocker) CopyFromContainer(_ context.Context, id, src string) (io.ReadCloser, container.PathStat, error) {
	f.record("read " + id + " " + src)
	return io.NopCloser(bytes.NewReader(f.autosave)), container.PathStat{}, nil
}

type recordedNotice struct {
	active bool
	in     notices.RaiseInput
}

type noticeLog struct{ got []recordedNotice }

func (n *noticeLog) Set(_ context.Context, active bool, in notices.RaiseInput) error {
	n.got = append(n.got, recordedNotice{active, in})
	return nil
}

func autosaveTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte(`{"admin":{"listen":"127.0.0.1:2019"}}`)
	if err := tw.WriteHeader(&tar.Header{Name: "autosave.json", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// adminAPI answers to any admin ID and counts /load calls per server.
type adminAPI struct {
	mu    sync.Mutex
	loads int
	body  string
	// refuseLoad makes /load fail, as a config Caddy rejects would.
	refuseLoad bool
}

func (a *adminAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/load" {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.refuseLoad {
			http.Error(w, "rejected", http.StatusBadRequest)
			return
		}
		a.loads++
		a.body = string(b)
		return
	}
	_, _ = w.Write([]byte(`{}`))
}

func (a *adminAPI) loaded() (int, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loads, a.body
}

// serveSocket starts api on the service's admin socket.
func serveSocket(t *testing.T, s *Service, api http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", s.adminSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func legacyContainer(id string) *container.InspectResponse {
	return &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         id,
			State:      &container.State{Running: true},
			HostConfig: &container.HostConfig{Binds: append([]string(nil), proxyVolumes...)},
		},
		Config: &container.Config{Labels: map[string]string{
			"dockmesh.component": "proxy",
			adminIDLabel:         "dockmesh-admin-0123456789abcdef0123456789abcdef",
		}},
	}
}

func newSwitchService(t *testing.T) (*Service, *noticeLog) {
	t.Helper()
	s := newServiceDB(t)
	// Not t.TempDir: its long path doesn't fit a unix socket address.
	dir, err := os.MkdirTemp("", "dmsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s.SetAdminSocketDir(dir)
	if err := s.ensureSocketDir(); err != nil {
		t.Fatal(err)
	}
	s.pullImage = func(context.Context, string) error { return nil }
	log := &noticeLog{}
	s.SetNotices(log)

	oldTimeout, oldLegacy := adminReadyTimeout, legacyEP
	adminReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { adminReadyTimeout, legacyEP = oldTimeout, oldLegacy; adminEP.Store(nil) })
	return s, log
}

func useLegacyServer(t *testing.T, api http.Handler) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	legacyEP = &adminEndpoint{url: srv.URL}
}

func switchNotice(log *noticeLog) *recordedNotice {
	for i := len(log.got) - 1; i >= 0; i-- {
		if log.got[i].in.Key == noticeKeyAdminSwitch {
			return &log.got[i]
		}
	}
	return nil
}

func TestMoveAdminToSocket(t *testing.T) {
	s, log := newSwitchService(t)
	api := &adminAPI{}
	f := &fakeDocker{autosave: autosaveTar(t), onStart: func(string) { serveSocket(t, s, api) }}
	old := legacyContainer("old")

	if err := s.moveAdminToSocket(context.Background(), f, "caddy:test", old); err != nil {
		t.Fatal(err)
	}
	if s.legacyAdmin.Load() || !s.Enabled() {
		t.Fatalf("legacy=%v enabled=%v after a successful move", s.legacyAdmin.Load(), s.Enabled())
	}
	loads, body := api.loaded()
	if loads != 1 || !strings.Contains(body, "admin.sock") || strings.Contains(body, "2019") {
		t.Fatalf("loads=%d body=%s", loads, body)
	}
	for _, want := range []string{"create " + nextContainerName, "stop old", "start new", "remove old", "rename new " + ProxyContainerName} {
		if !slices.Contains(f.calls, want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	if n := switchNotice(log); n == nil || n.active {
		t.Fatalf("notice = %+v, want resolved", n)
	}
}

// A failure before the old container is stopped must not touch it.
func TestMoveAdminPreflightFailureLeavesOldRunning(t *testing.T) {
	s, log := newSwitchService(t)
	legacy := &adminAPI{}
	useLegacyServer(t, legacy)
	f := &fakeDocker{autosave: autosaveTar(t), failCreate: true}

	err := s.moveAdminToSocket(context.Background(), f, "caddy:test", legacyContainer("old"))
	if !errors.Is(err, ErrAdminSwitchFailed) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "remove") {
			t.Fatalf("old container touched: %v", f.calls)
		}
	}
	assertKeptOld(t, s, log, legacy)
}

func assertKeptOld(t *testing.T, s *Service, log *noticeLog, legacy *adminAPI) {
	t.Helper()
	if !s.legacyAdmin.Load() || !s.Enabled() {
		t.Fatalf("legacy=%v enabled=%v, want the old container managed", s.legacyAdmin.Load(), s.Enabled())
	}
	// Pushing the socket listen address to the old container would lock
	// Dockmesh out of it.
	if loads, body := legacy.loaded(); loads == 0 || !strings.Contains(body, legacyAdminAddr) || strings.Contains(body, "admin.sock") {
		t.Fatalf("legacy loads=%d body=%s", loads, body)
	}
	n := switchNotice(log)
	if n == nil || !n.active || !strings.Contains(n.in.Body, "restart the Dockmesh service") {
		t.Fatalf("notice = %+v", n)
	}
}

func TestMoveAdminRollsBackWhenNewContainerFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Service, f *fakeDocker)
	}{
		{"does not start", func(_ *testing.T, _ *Service, f *fakeDocker) { f.failStartID = "new" }},
		{"admin never answers", func(*testing.T, *Service, *fakeDocker) {}},
		{"config rejected", func(t *testing.T, s *Service, f *fakeDocker) {
			f.onStart = func(id string) {
				if id == "new" {
					serveSocket(t, s, &adminAPI{refuseLoad: true})
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, log := newSwitchService(t)
			legacy := &adminAPI{}
			useLegacyServer(t, legacy)
			f := &fakeDocker{autosave: autosaveTar(t)}
			tc.setup(t, s, f)

			err := s.moveAdminToSocket(context.Background(), f, "caddy:test", legacyContainer("old"))
			if !errors.Is(err, ErrAdminSwitchFailed) {
				t.Fatalf("err = %v", err)
			}
			for _, want := range []string{"stop old", "remove new", "copy old /config/caddy", "start old"} {
				if !slices.Contains(f.calls, want) {
					t.Errorf("missing %q in %v", want, f.calls)
				}
			}
			if slices.Contains(f.calls, "remove old") {
				t.Fatalf("old container removed: %v", f.calls)
			}
			assertKeptOld(t, s, log, legacy)
		})
	}
}

// A version switch would restart the old container through the move.
func TestSwitchVersionWaitsForAdminMove(t *testing.T) {
	s, _ := newSwitchService(t)
	s.settings = &memSettings{}
	s.useLegacyAdmin()
	_, err := s.SwitchVersion(context.Background(), TestedVersions[0].Version)
	if !errors.Is(err, ErrAdminSwitchPending) {
		t.Fatalf("err = %v", err)
	}
}

func TestAdoptNextContainer(t *testing.T) {
	cases := []struct {
		name   string
		exists map[string]bool
		want   string
	}{
		{"nothing pending", map[string]bool{ProxyContainerName: true}, ""},
		{"old removed before the rename", map[string]bool{nextContainerName: true}, "rename " + nextContainerName + " " + ProxyContainerName},
		{"old still there", map[string]bool{nextContainerName: true, ProxyContainerName: true}, "remove " + nextContainerName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDocker{exists: tc.exists}
			s := NewService(nil, nil, false)
			if err := s.adoptNextContainer(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && len(f.calls) != 0 || tc.want != "" && !slices.Contains(f.calls, tc.want) {
				t.Fatalf("calls = %v, want %q", f.calls, tc.want)
			}
		})
	}
}

func TestUsesTCPAdmin(t *testing.T) {
	s := NewService(nil, nil, false)
	s.SetAdminSocketDir(t.TempDir())
	if !usesTCPAdmin(legacyContainer("x")) {
		t.Error("container without the socket bind not recognised")
	}
	current := legacyContainer("x")
	current.HostConfig.Binds = s.proxyBinds()
	if usesTCPAdmin(current) {
		t.Error("container with the socket bind treated as legacy")
	}
	foreign := legacyContainer("x")
	foreign.Config.Labels = map[string]string{}
	if usesTCPAdmin(foreign) {
		t.Error("foreign container treated as legacy")
	}
}

func TestEnsureSocketDir(t *testing.T) {
	short := func(t *testing.T) string {
		t.Helper()
		d, err := os.MkdirTemp("", "dm")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		return d
	}
	t.Run("created private", func(t *testing.T) {
		s := NewService(nil, nil, false)
		s.SetAdminSocketDir(short(t))
		if err := s.ensureSocketDir(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(s.adminSocketDir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("mode = %v, err = %v", info.Mode(), err)
		}
	})
	t.Run("tightens an open one", func(t *testing.T) {
		s := NewService(nil, nil, false)
		s.SetAdminSocketDir(short(t))
		if err := os.Mkdir(s.adminSocketDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := s.ensureSocketDir(); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(s.adminSocketDir); info.Mode().Perm() != 0o700 {
			t.Fatalf("mode = %v", info.Mode())
		}
	})
	t.Run("refuses a symlink", func(t *testing.T) {
		s := NewService(nil, nil, false)
		base := short(t)
		s.SetAdminSocketDir(base)
		if err := os.Symlink(short(t), s.adminSocketDir); err != nil {
			t.Fatal(err)
		}
		if err := s.ensureSocketDir(); err == nil {
			t.Fatal("symlink accepted")
		}
	})
	t.Run("refuses a path too long for a socket", func(t *testing.T) {
		s := NewService(nil, nil, false)
		s.SetAdminSocketDir("/" + strings.Repeat("a", maxSocketPath))
		if err := s.ensureSocketDir(); err == nil || !strings.Contains(err.Error(), "too long") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refuses a colon", func(t *testing.T) {
		s := NewService(nil, nil, false)
		s.SetAdminSocketDir("/data:x")
		if err := s.ensureSocketDir(); err == nil || !strings.Contains(err.Error(), "colon") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDialSocketRefusesNonSocket(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, link, filepath.Join(dir, "missing")} {
		if c, err := dialSocket(context.Background(), p); err == nil {
			_ = c.Close()
			t.Errorf("%s: dial succeeded", p)
		}
	}
}

// The admin requests go through the socket, with the Host and Origin
// Caddy's enforce_origin accepts.
func TestAdminRequestsUseTheSocket(t *testing.T) {
	s, _ := newSwitchService(t)
	var host, origin string
	serveSocket(t, s, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		host, origin = r.Host, r.Header.Get("Origin")
	}))
	s.useSocketAdmin()
	if _, err := runningConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host != adminHost || origin != "http://"+adminHost {
		t.Fatalf("host=%q origin=%q", host, origin)
	}
}

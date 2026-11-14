package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// releaseFixture serves a release of tag signed with a fresh key.
type releaseFixture struct {
	files map[string][]byte
	hits  map[string]int
	cache *ReleaseCache
}

func newReleaseFixture(t *testing.T, tag string, archive []byte) *releaseFixture {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := releasesig.New(pub.String())
	if err != nil {
		t.Fatal(err)
	}
	name, _ := ReleaseArchive("amd64")
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	f := &releaseFixture{files: map[string][]byte{
		tag + "/" + releasesig.ChecksumsFile: checksums,
		tag + "/" + releasesig.SignatureFile: minisign.SignWithComments(priv, checksums, releasesig.TrustedComment(tag), ""),
		tag + "/" + name:                     archive,
	}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path[1:]
		f.hits[p]++
		data, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	f.cache = &ReleaseCache{Dir: t.TempDir(), Version: tag, BaseURL: srv.URL + "/", Verifier: v}
	return f
}

func TestReleaseCacheEnsure(t *testing.T) {
	ctx := context.Background()
	f := newReleaseFixture(t, "v0.6.1", []byte("release archive"))
	if err := os.MkdirAll(filepath.Join(f.cache.Dir, "v0.6.0"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := f.cache.Ensure(ctx, "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := f.cache.Ensure(ctx, "amd64"); err != nil {
		t.Fatal(err)
	}
	if n := f.hits["v0.6.1/dockmesh_linux_amd64.tar.gz"]; n != 1 {
		t.Errorf("archive downloaded %d times, want once", n)
	}
	if _, err := os.Stat(filepath.Join(f.cache.Dir, "v0.6.0")); !os.IsNotExist(err) {
		t.Error("files of another version kept")
	}
	if err := f.cache.Ensure(ctx, "../x"); err == nil {
		t.Error("unsupported arch accepted")
	}
	dev := &ReleaseCache{Dir: t.TempDir(), Version: "dev"}
	if err := dev.Ensure(ctx, "amd64"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("dev build: %v", err)
	}
}

func TestReleaseCacheRefusesBadRelease(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(f *releaseFixture)
	}{
		{"signature of another tag", func(f *releaseFixture) {
			f.files["v0.6.1/"+releasesig.SignatureFile] = []byte("untrusted comment: x\nnot a signature\n")
		}},
		{"archive changed", func(f *releaseFixture) {
			f.files["v0.6.1/dockmesh_linux_amd64.tar.gz"] = []byte("other archive")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReleaseFixture(t, "v0.6.1", []byte("release archive"))
			tt.tamper(f)
			if err := f.cache.Ensure(context.Background(), "amd64"); err == nil {
				t.Fatal("bad release accepted")
			}
			if _, err := os.Stat(filepath.Join(f.cache.Dir, "v0.6.1", "dockmesh_linux_amd64.tar.gz")); !os.IsNotExist(err) {
				t.Error("bad archive kept in the cache")
			}
		})
	}
}

func TestReleaseHandlerServesOnlyTheServersRelease(t *testing.T) {
	svc, _ := newTestService(t)
	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := svc.Enroll(context.Background(), enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := parseCertForTest([]byte(enrolled.ClientCert))
	if err != nil {
		t.Fatal(err)
	}
	f := newReleaseFixture(t, "v0.6.1", []byte("release archive"))
	if err := f.cache.Ensure(context.Background(), "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.cache.Dir, "v0.6.1", "other"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	h := f.cache.Handler(svc)
	cases := []struct {
		name string
		path string
		peer *x509.Certificate
		want int
	}{
		{"no cert", ReleasePath + "v0.6.1/checksums.txt", nil, http.StatusUnauthorized},
		{"archive", ReleasePath + "v0.6.1/dockmesh_linux_amd64.tar.gz", agentCert, http.StatusOK},
		{"signature", ReleasePath + "v0.6.1/checksums.txt.minisig", agentCert, http.StatusOK},
		{"other version", ReleasePath + "v0.6.0/checksums.txt", agentCert, http.StatusNotFound},
		{"other file", ReleasePath + "v0.6.1/other", agentCert, http.StatusNotFound},
		{"traversal", ReleasePath + "v0.6.1/../v0.6.1/checksums.txt", agentCert, http.StatusNotFound},
		{"not cached", ReleasePath + "v0.6.1/dockmesh_linux_arm64.tar.gz", agentCert, http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.TLS = &tls.ConnectionState{}
		if c.peer != nil {
			req.TLS.PeerCertificates = []*x509.Certificate{c.peer}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestUpgradeWaitsForStacksOnTheHost(t *testing.T) {
	c := NewUpgradeController(&Service{connected: map[string]*ConnectedAgent{}}, nil)
	c.manualLockWait = 50 * time.Millisecond
	locks := stacklock.New()
	c.SetLocks(locks)
	ag := &ConnectedAgent{ID: "agent-1", Hello: HelloPayload{Arch: "amd64", Layout: LayoutManaged}}
	c.agents.connected[ag.ID] = ag
	f := newReleaseFixture(t, "v9.9.9", []byte("release archive"))
	c.SetReleaseCache(f.cache)
	defer setServerVersion(t, "v9.9.9")()

	release, err := locks.TryLock(stacklock.HostKey("agent-1", "web"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := c.UpgradeOne(context.Background(), "agent-1"); !errors.Is(err, ErrHostBusy) {
		t.Fatalf("upgrade during a deploy: %v", err)
	}
	// The wait gave the host back, so the deploy's stack is not stuck.
	if _, err := locks.TryLock(stacklock.HostKey("agent-1", "db"), stacklock.OpDeploy); err != nil {
		t.Errorf("host still held after the wait: %v", err)
	}
}

func TestManagedUpgradeReqCarriesOnlyTheVersion(t *testing.T) {
	c := NewUpgradeController(&Service{}, nil)
	f := newReleaseFixture(t, "v9.9.9", []byte("release archive"))
	c.SetReleaseCache(f.cache)
	defer setServerVersion(t, "v9.9.9")()
	req, err := c.buildUpgradeReq(context.Background(), &ConnectedAgent{Hello: HelloPayload{Arch: "amd64", Layout: LayoutManaged}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Version != "v9.9.9" || req.SHA256 != "" || req.BinaryURL != "" {
		t.Errorf("managed request %+v", req)
	}
	if _, err := os.Stat(filepath.Join(f.cache.Dir, "v9.9.9", "dockmesh_linux_amd64.tar.gz")); err != nil {
		t.Errorf("release not cached before the dispatch: %v", err)
	}
}

func setServerVersion(t *testing.T, v string) func() {
	t.Helper()
	old := version.Version
	version.Version = v
	return func() { version.Version = old }
}

func TestReleaseCacheBacksOffAfterAFailedFetch(t *testing.T) {
	f := newReleaseFixture(t, "v0.6.1", []byte("release archive"))
	f.files["v0.6.1/dockmesh_linux_amd64.tar.gz"] = []byte("other archive")
	for range 3 {
		if err := f.cache.Ensure(context.Background(), "amd64"); err == nil {
			t.Fatal("bad archive accepted")
		}
	}
	if n := f.hits["v0.6.1/dockmesh_linux_amd64.tar.gz"]; n != 1 {
		t.Errorf("archive fetched %d times within the retry gap, want once", n)
	}
}

func TestClipAgentText(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"a\nb\x1bc", 10, "a b c"},
		{"äöüäöü", 3, "äöü…"},
		{"ok\xffbad", 10, "ok\uFFFDbad"},
	}
	for _, tt := range tests {
		if got := clip(tt.in, tt.n); got != tt.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

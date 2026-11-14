package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

// ReleasePath is the prefix under which the agent listener serves the
// signed release of the server's version to managed agents:
// <ReleasePath><tag>/<file>.
const ReleasePath = "/agent-release/"

// Size caps for the release files the server fetches and serves.
const (
	maxReleaseChecksums = rootupdate.MaxChecksumsSize
	maxReleaseSignature = rootupdate.MaxSignatureSize
	maxReleaseArchive   = rootupdate.DefaultMaxArtifactSize
)

// fetchRetryGap is how long a failed fetch keeps Ensure from fetching that
// version again.
const fetchRetryGap = 10 * time.Minute

// ReleaseArchive is the release tarball holding the agent binary for arch.
func ReleaseArchive(arch string) (string, error) {
	if _, err := AgentBinaryName(arch); err != nil {
		return "", err
	}
	return "dockmesh_linux_" + arch + ".tar.gz", nil
}

// ReleaseCache keeps the signed release files of one version below Dir,
// fetched from BaseURL on first use and checked with Verifier, so managed
// agents without internet access get them over their mTLS channel.
type ReleaseCache struct {
	Dir      string
	Version  string
	BaseURL  string
	Client   *http.Client
	Verifier *releasesig.Verifier

	mu         sync.Mutex
	failedAt   time.Time
	failedWith error
}

// ErrNoRelease means a managed agent can't be updated from this build.
var ErrNoRelease = errors.New("this server build has no signed release to update agents from")

// Ensure makes sure checksums, signature and the archive for arch of the
// server's version are in the cache and verified.
func (c *ReleaseCache) Ensure(ctx context.Context, arch string) error {
	if c == nil || !releasesig.ValidTag(c.Version) {
		return ErrNoRelease
	}
	archive, err := ReleaseArchive(arch)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failedWith != nil && time.Since(c.failedAt) < fetchRetryGap {
		return c.failedWith
	}
	if err := c.ensure(ctx, archive); err != nil {
		c.failedAt, c.failedWith = time.Now(), err
		return err
	}
	c.failedWith = nil
	return nil
}

func (c *ReleaseCache) ensure(ctx context.Context, archive string) error {
	dir := filepath.Join(c.Dir, c.Version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	c.dropOtherVersions()
	checksums, err := c.file(ctx, dir, releasesig.ChecksumsFile, maxReleaseChecksums)
	if err != nil {
		return err
	}
	sig, err := c.file(ctx, dir, releasesig.SignatureFile, maxReleaseSignature)
	if err != nil {
		return err
	}
	if _, err := c.Verifier.VerifyChecksums(checksums, sig, c.Version); err != nil {
		c.remove(dir, releasesig.ChecksumsFile, releasesig.SignatureFile)
		return fmt.Errorf("release %s: %w", c.Version, err)
	}
	want, err := releasesig.Checksum(checksums, archive)
	if err != nil {
		return err
	}
	if ok, err := fileHasSHA256(filepath.Join(dir, archive), want); err == nil && ok {
		return nil
	}
	return c.download(ctx, dir, archive, want)
}

// file returns name from the cache, downloading it when it is missing.
func (c *ReleaseCache) file(ctx context.Context, dir, name string, max int64) ([]byte, error) {
	p := filepath.Join(dir, name)
	if data, err := readCapped(p, max); err == nil {
		return data, nil
	}
	resp, err := c.get(ctx, name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("download %s: larger than %d bytes", name, max)
	}
	if err := WriteFileAtomic(dir, name, bytes.NewReader(data), 0o640); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *ReleaseCache) download(ctx context.Context, dir, name string, want []byte) error {
	resp, err := c.get(ctx, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxReleaseArchive+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if n > maxReleaseArchive {
		return fmt.Errorf("download %s: larger than %d bytes", name, maxReleaseArchive)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("download %s: %w", name, releasesig.ErrChecksum)
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func (c *ReleaseCache) get(ctx context.Context, name string) (*http.Response, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+c.Version+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Dockmesh/"+c.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}
	return resp, nil
}

// dropOtherVersions removes the files of versions the server no longer
// runs; only its own version is ever served.
func (c *ReleaseCache) dropOtherVersions() {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != c.Version {
			_ = os.RemoveAll(filepath.Join(c.Dir, e.Name()))
		}
	}
}

func (c *ReleaseCache) remove(dir string, names ...string) {
	for _, n := range names {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// Handler serves the cached files to clients holding a valid, non-revoked
// agent cert. Only the server's version and the three release file names
// are served.
func (c *ReleaseCache) Handler(svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		ag, err := svc.LookupByFingerprint(r.Context(), pki.FingerprintFromCert(r.TLS.PeerCertificates[0]))
		if err != nil || ag.Status == "revoked" {
			http.Error(w, "agent not registered", http.StatusUnauthorized)
			return
		}
		tag, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, ReleasePath), "/")
		if !ok || c == nil || tag != c.Version || !releaseFileName(name) {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(filepath.Join(c.Dir, tag))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		f, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, name, st.ModTime(), f)
	})
}

func releaseFileName(name string) bool {
	if name == releasesig.ChecksumsFile || name == releasesig.SignatureFile {
		return true
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if a, _ := ReleaseArchive(arch); name == a {
			return true
		}
	}
	return false
}

func readCapped(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return data, nil
}

func fileHasSHA256(p string, want []byte) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return bytes.Equal(h.Sum(nil), want), nil
}

// WriteFileAtomic replaces name in dir with the content of r through a
// temp file and a rename.
func WriteFileAtomic(dir, name string, r io.Reader, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

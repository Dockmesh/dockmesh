package agents

import (
	"crypto/sha256"
	"encoding/hex"
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
)

// ErrNoAgentBinary means the server has no bundled agent binary for the
// requested architecture.
var ErrNoAgentBinary = errors.New("agent binary not bundled with this server build")

// BinaryDir is where the server looks for the cross-compiled agent and
// dmctl binaries it serves under /install/.
func BinaryDir() string {
	if d := os.Getenv("DOCKMESH_BINARY_DIR"); d != "" {
		return d
	}
	return "./bin"
}

// AgentBinaryName maps a Go architecture to the bundled agent file name.
// Only architectures the release builds are accepted, so a hello payload
// can't steer the server to an arbitrary file.
func AgentBinaryName(arch string) (string, error) {
	switch arch {
	case "amd64", "arm64":
		return "dockmesh-agent-linux-" + arch, nil
	}
	return "", fmt.Errorf("unsupported agent architecture %q", arch)
}

type binarySum struct {
	size    int64
	modTime time.Time
	sum     string
}

var (
	binarySumMu    sync.Mutex
	binarySumCache = map[string]binarySum{}
)

// AgentBinarySHA256 returns the hex SHA-256 of the bundled agent binary
// for arch. The result is cached per file and recomputed when the file's
// size or mtime changes (e.g. after `make agent-bundle`).
func AgentBinarySHA256(arch string) (string, error) {
	name, err := AgentBinaryName(arch)
	if err != nil {
		return "", err
	}
	path := filepath.Join(BinaryDir(), name)
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrNoAgentBinary, name)
	}
	if err != nil {
		return "", err
	}

	binarySumMu.Lock()
	defer binarySumMu.Unlock()
	if c, ok := binarySumCache[path]; ok && c.size == st.Size() && c.modTime.Equal(st.ModTime()) {
		return c.sum, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	binarySumCache[path] = binarySum{size: st.Size(), modTime: st.ModTime(), sum: sum}
	return sum, nil
}

// BinaryPath is the prefix under which the agent listener serves agent
// binaries to enrolled agents.
const BinaryPath = "/agent-binary/"

// BinaryHandler serves bundled agent binaries on the agent listener to
// clients holding a valid, non-revoked agent cert, so upgrades travel
// over the same pinned mTLS channel as everything else.
func BinaryHandler(svc *Service) http.Handler {
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
		name := strings.TrimPrefix(r.URL.Path, BinaryPath)
		known := false
		for _, arch := range []string{"amd64", "arm64"} {
			if n, _ := AgentBinaryName(arch); n == name {
				known = true
			}
		}
		if !known {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(BinaryDir(), name))
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, ErrNoAgentBinary.Error(), http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, "open binary", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "stat binary", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, name, st.ModTime(), f)
	})
}

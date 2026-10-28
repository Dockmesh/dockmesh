package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
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

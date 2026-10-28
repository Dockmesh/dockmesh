package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

const maxAgentBinaryBytes = 256 << 20

var (
	upgradeMu      sync.Mutex
	sha256HexRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errUpgradeBusy = errors.New("an upgrade is already in progress")
	errSameBinary  = errors.New("already running this agent build")
)

// agentSelfUpgrade downloads the binary named in req, verifies it against
// the SHA-256 the server sent over the mTLS channel and atomically
// replaces the running executable. The caller restarts the process.
// On success the upgrade lock stays held so a second request can't race
// the pending restart.
func agentSelfUpgrade(req agents.AgentUpgradeReq) error {
	if !upgradeMu.TryLock() {
		return errUpgradeBusy
	}
	exePath, err := os.Executable()
	if err != nil {
		upgradeMu.Unlock()
		return fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	if err := upgradeBinary(client, req, exePath, runtime.GOARCH); err != nil {
		upgradeMu.Unlock()
		return err
	}
	slog.Info("agent upgrade: binary replaced", "path", exePath, "version", req.Version)
	return nil
}

func upgradeBinary(client *http.Client, req agents.AgentUpgradeReq, exePath, arch string) error {
	want := strings.ToLower(strings.TrimSpace(req.SHA256))
	if !sha256HexRe.MatchString(want) {
		return errors.New("upgrade request carries no valid sha256 — server too old to upgrade this agent safely")
	}
	if err := checkUpgradeURL(req.BinaryURL, arch); err != nil {
		return err
	}
	if cur, err := fileSHA256(exePath); err == nil && cur == want {
		return errSameBinary
	}

	slog.Info("agent upgrade: downloading", "url", req.BinaryURL, "version", req.Version)
	resp, err := client.Get(req.BinaryURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// A unique temp file in the executable's directory keeps the final
	// rename atomic (same filesystem) and avoids two writers sharing a
	// fixed name.
	tmp, err := os.CreateTemp(filepath.Dir(exePath), ".dockmesh-agent-upgrade-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxAgentBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if n > maxAgentBinaryBytes {
		return fmt.Errorf("download: binary larger than %d bytes", maxAgentBinaryBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", want, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	committed = true
	return nil
}

// checkUpgradeURL limits the download to the agent binary for this
// architecture over http(s). The checksum is what makes the download
// trustworthy; this only keeps a confused server from pointing the agent
// at unrelated URLs or the wrong architecture.
func checkUpgradeURL(raw, arch string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("binary_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("binary_url: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" || u.User != nil {
		return errors.New("binary_url: invalid host")
	}
	want := "dockmesh-agent-linux-" + arch
	if path.Base(u.Path) != want {
		return fmt.Errorf("binary_url: expected %s for this host", want)
	}
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

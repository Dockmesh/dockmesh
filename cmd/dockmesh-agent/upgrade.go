package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
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

// upgradeOrigin is the agent listener the binary is fetched from, with
// the agent's own mTLS config. It is set once at startup from local
// configuration, so the download target never comes from a server frame.
var upgradeOrigin struct {
	dialURL string
	tls     *tls.Config
}

// agentSelfUpgrade downloads this architecture's binary from the agent
// listener over mTLS, verifies it against the SHA-256 the server sent and
// atomically replaces the running executable. The caller restarts the
// process. On success the upgrade lock stays held so a second request
// can't race the pending restart.
func agentSelfUpgrade(req agents.AgentUpgradeReq) error {
	if !upgradeMu.TryLock() {
		return errUpgradeBusy
	}
	binURL, err := binaryURL(upgradeOrigin.dialURL, runtime.GOARCH)
	if err != nil {
		upgradeMu.Unlock()
		return err
	}
	exePath, err := os.Executable()
	if err != nil {
		upgradeMu.Unlock()
		return fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	client := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: upgradeOrigin.tls},
	}
	if err := upgradeBinary(client, binURL, req.SHA256, exePath); err != nil {
		upgradeMu.Unlock()
		return err
	}
	slog.Info("agent upgrade: binary replaced", "path", exePath, "version", req.Version)
	return nil
}

// binaryURL maps the wss:// agent URL to the listener's binary endpoint.
func binaryURL(dialURL, arch string) (string, error) {
	u, err := url.Parse(dialURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("agent url %q: not usable for upgrades", dialURL)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("agent url %q: unsupported scheme", dialURL)
	}
	u.Path = agents.BinaryPath + "dockmesh-agent-linux-" + arch
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func upgradeBinary(client *http.Client, binURL, wantSHA, exePath string) error {
	want := strings.ToLower(strings.TrimSpace(wantSHA))
	if !sha256HexRe.MatchString(want) {
		return errors.New("upgrade request carries no valid sha256 — server too old to upgrade this agent safely")
	}
	if cur, err := fileSHA256(exePath); err == nil && cur == want {
		return errSameBinary
	}

	slog.Info("agent upgrade: downloading", "url", binURL)
	resp, err := client.Get(binURL)
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
	slog.Info("agent upgrade: checksum verified", "sha256", want, "bytes", n)
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

// openBinDir lets other users run the agent binary through the
// /usr/local/bin symlink (for `dockmesh-agent --version`): the data dir
// becomes traversable without being listable, and bin/ readable. Older
// installers created them as 0700 and 0750; other modes are left alone.
// The keys in the data dir keep their own 0600. A data dir shared with a
// server on the same host is never touched.
func openBinDir(dataDir string) {
	for _, f := range []string{"dockmesh.db", "dockmesh.env", filepath.Join("data", "dockmesh.db")} {
		if _, err := os.Stat(filepath.Join(dataDir, f)); err == nil {
			return
		}
	}
	for _, d := range []struct {
		path     string
		old, new os.FileMode
	}{
		{dataDir, 0o700, 0o711},
		{filepath.Join(dataDir, "bin"), 0o750, 0o755},
	} {
		fi, err := os.Stat(d.path)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != d.old {
			continue
		}
		if err := os.Chmod(d.path, d.new); err != nil {
			slog.Warn("open agent binary dir", "path", d.path, "err", err)
		}
	}
}

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/semver"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// Managed layout: the binary belongs to root and the root oneshot behind
// managedPathUnit installs what the agent stages in its update dir.
const (
	managedPathUnit    = "/etc/systemd/system/dockmesh-agent-update.path"
	managedServiceUnit = "/etc/systemd/system/dockmesh-agent-update.service"
	agentUnitFile      = "/etc/systemd/system/dockmesh-agent.service"
	agentUnitName      = "dockmesh-agent.service"
	managedStateDir    = "/var/lib/dockmesh-agent-update"
	// runningFile tells the root oneshot which version connected to the
	// server; it is the agent's health check.
	runningFile = "running"
)

// Sizes the agent accepts from the server for a managed update; the root
// oneshot applies the same caps again.
const (
	maxManagedArchive = rootupdate.DefaultMaxArtifactSize
	// stagedRequestLimit is how long a request may wait for the root
	// oneshot before the agent drops it and takes new requests again.
	stagedRequestLimit = 5 * time.Minute
)

var errNotNewer = errors.New("the requested version is not newer than this agent")

// managedVerifier checks staged releases; tests swap in their own key.
var managedVerifier = releasesig.Default

// agentLayout is the layout this process was started in, set once by
// detectLayout.
var agentLayout = agents.LayoutLegacy

// updateDir is where a managed agent stages a release for the oneshot.
func updateDir(dataDir string) string { return filepath.Join(dataDir, "update") }

// detectLayout reports managed when the running binary is the root-owned
// pathBinary, which this account can't write, and the oneshot's path unit
// is installed. Everything else, including agents running as root, keeps
// replacing itself.
func detectLayout() string {
	exe, err := os.Executable()
	if err != nil {
		return agents.LayoutLegacy
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if exe != pathBinary || os.Geteuid() == 0 || !rootOwnedBinary(exe) {
		return agents.LayoutLegacy
	}
	if _, err := os.Stat(managedPathUnit); err != nil {
		return agents.LayoutLegacy
	}
	return agents.LayoutManaged
}

// managedUpgrade downloads the signed release of req.Version from the
// agent listener, checks it with this binary's keys and asks the root
// oneshot to install it. The oneshot stops and restarts the agent. On
// success the upgrade lock stays held until the oneshot took the request
// or stagedRequestLimit passed.
func managedUpgrade(dataDir string, req agents.AgentUpgradeReq) error {
	if !upgradeMu.TryLock() {
		return errUpgradeBusy
	}
	if err := stageManaged(dataDir, req.Version); err != nil {
		upgradeMu.Unlock()
		return err
	}
	slog.Info("agent upgrade: release staged for the root updater", "version", req.Version)
	go releaseWhenTaken(updateDir(dataDir))
	return nil
}

func stageManaged(dataDir, tag string) error {
	if !releasesig.ValidTag(tag) {
		return fmt.Errorf("upgrade request carries no release version: %q", tag)
	}
	if !semver.Newer(tag, version.Version) {
		if strings.TrimPrefix(tag, "v") == strings.TrimPrefix(version.Version, "v") {
			return errSameBinary
		}
		return errNotNewer
	}
	dir := updateDir(dataDir)
	if err := os.Mkdir(dir, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if _, err := os.Lstat(filepath.Join(dir, rootupdate.PendingFile)); err == nil {
		return errUpgradeBusy
	}
	client := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: upgradeOrigin.tls},
	}
	base, err := releaseURL(upgradeOrigin.dialURL, tag)
	if err != nil {
		return err
	}
	checksums, err := fetchSmall(client, base+releasesig.ChecksumsFile, rootupdate.MaxChecksumsSize)
	if err != nil {
		return err
	}
	sig, err := fetchSmall(client, base+releasesig.SignatureFile, rootupdate.MaxSignatureSize)
	if err != nil {
		return err
	}
	if _, err := managedVerifier().VerifyChecksums(checksums, sig, tag); err != nil {
		return err
	}
	archive := managedArchive()
	want, err := releasesig.Checksum(checksums, archive)
	if err != nil {
		return err
	}
	if err := fetchArchive(client, base+archive, dir, archive, want); err != nil {
		return err
	}
	for name, data := range map[string][]byte{releasesig.ChecksumsFile: checksums, releasesig.SignatureFile: sig} {
		if err := writeStaged(dir, name, data); err != nil {
			return err
		}
	}
	reqBody, _ := json.Marshal(rootupdate.Request{Version: tag})
	return writeStaged(dir, rootupdate.RequestFile, reqBody)
}

// releaseWhenTaken frees the upgrade lock when the oneshot never took the
// request, e.g. because its path unit is stopped, and drops the request.
func releaseWhenTaken(dir string) {
	deadline := time.Now().Add(stagedRequestLimit)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(filepath.Join(dir, rootupdate.RequestFile)); errors.Is(err, os.ErrNotExist) {
			// Taken: the oneshot stops this process.
			return
		}
		time.Sleep(2 * time.Second)
	}
	slog.Warn("agent upgrade: the root updater did not take the request; check dockmesh-agent-update.path")
	for _, name := range []string{rootupdate.RequestFile, managedArchive(), releasesig.ChecksumsFile, releasesig.SignatureFile} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	upgradeMu.Unlock()
}

func managedArchive() string { return "dockmesh_linux_" + runtime.GOARCH + ".tar.gz" }

// releaseURL maps the wss:// agent URL to the listener's release files of
// tag.
func releaseURL(dialURL, tag string) (string, error) {
	u, err := url.Parse(dialURL)
	if err != nil || u.Host == "" || u.Scheme != "wss" {
		return "", fmt.Errorf("agent url %q: not usable for managed updates", dialURL)
	}
	u.Scheme = "https"
	u.Path = agents.ReleasePath + tag + "/"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	return u.String(), nil
}

func fetchSmall(client *http.Client, u string, max int64) ([]byte, error) {
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", filepath.Base(u), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("download %s: larger than %d bytes", filepath.Base(u), max)
	}
	return data, nil
}

func fetchArchive(client *http.Client, u, dir, name string, want []byte) error {
	resp, err := client.Get(u)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxManagedArchive+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if n > maxManagedArchive {
		return fmt.Errorf("download %s: larger than %d bytes", name, maxManagedArchive)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("download %s: %w", name, releasesig.ErrChecksum)
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func writeStaged(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o640)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// markRunning tells the root oneshot that this version is connected.
func markRunning(dataDir string) {
	dir := updateDir(dataDir)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return
	}
	if err := writeStaged(dir, runningFile, []byte(version.Version)); err != nil {
		slog.Warn("agent update: write health file", "err", err)
	}
}

// lastUpdateResult reads the root oneshot's result for the hello; nil
// without one.
func lastUpdateResult(dataDir string) *agents.AgentUpdateResult {
	f, err := os.OpenFile(filepath.Join(updateDir(dataDir), rootupdate.ResultFile), os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	var res rootupdate.Result
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&res); err != nil || res.Status == "" {
		return nil
	}
	return &agents.AgentUpdateResult{Status: string(res.Status), From: res.From, To: res.To, Step: res.Step, Reason: res.Reason}
}

// clearUpdateResult removes the result once a server got it. A failed
// rollback stays, so every connect reports it until someone finishes it.
func clearUpdateResult(dataDir string, res *agents.AgentUpdateResult) {
	if res == nil || res.Status == string(rootupdate.StatusFailed) {
		return
	}
	_ = os.Remove(filepath.Join(updateDir(dataDir), rootupdate.ResultFile))
}

//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/pkg/version"
)

const (
	oNoFollow  = syscall.O_NOFOLLOW
	oDirectory = syscall.O_DIRECTORY
)

// agentAccount runs a managed agent.
const agentAccount = "dockmesh-agent"

// rootOwnedBinary reports whether p belongs to root and only root may
// write it.
func rootOwnedBinary(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}

// runUpdateCmd is `dockmesh-agent update apply`, the root oneshot behind
// dockmesh-agent-update.path.
func runUpdateCmd(args []string) {
	if len(args) == 0 || args[0] != "apply" {
		fmt.Fprintln(os.Stderr, "usage: dockmesh-agent update apply --data-dir <dir>")
		os.Exit(2)
	}
	if err := updateApply(args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "update apply:", err)
		os.Exit(1)
	}
}

func updateApply(args []string) error {
	fs := flag.NewFlagSet("update apply", flag.ExitOnError)
	data := fs.String("data-dir", "", "data dir of the agent (set by dockmesh-agent-update.service)")
	_ = fs.Parse(args)
	if runtime.GOOS != "linux" {
		return errors.New("updates are applied by systemd on Linux only")
	}
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}
	if !filepath.IsAbs(*data) {
		return errors.New("--data-dir needs an absolute path")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return err
	}
	if binary != pathBinary || !rootOwnedBinary(binary) {
		return fmt.Errorf("the managed agent binary must be the root-owned %s, not %s", pathBinary, binary)
	}
	if err := legacyCopyStepAt(filepath.Clean(*data), legacyRefreshScript, agentUnitFile, true); err != nil {
		return fmt.Errorf("not a managed agent: %w", err)
	}
	uid, err := agentServiceUID()
	if err != nil {
		return err
	}
	reqDir := updateDir(filepath.Clean(*data))
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// An interrupted update is finished first: a stacks-dir request must
	// never keep a rollback from running.
	req := requestOf(reqDir, uid)
	if req.Type == rootupdate.RequestStacksDirApply && rootupdate.Unfinished(managedStateDir) {
		// Removed first: the path unit would start the request again once
		// the recovery below is over.
		if err := rootupdate.RemoveServiceFile(reqDir, uid, rootupdate.RequestFile); err != nil {
			return err
		}
		(&stacksDirApplier{reqDir: reqDir, uid: uid, log: logger}).result(agents.StacksDirRefused,
			agents.StacksDirCheck{Path: req.Path}, errors.New("an interrupted update is finished first"))
	}
	if req.Type == rootupdate.RequestStacksDirApply && !rootupdate.Unfinished(managedStateDir) {
		gid := 0
		if uid != 0 {
			if _, gid, err = lookupAccount(agentAccount); err != nil {
				return err
			}
		}
		in := hostInstaller(binary)
		a := &stacksDirApplier{
			dataDir: filepath.Clean(*data), reqDir: reqDir, uid: uid, gid: gid, stateDir: managedStateDir, log: logger,
			rules: func(dirs stackDirs) stacksDirRules {
				env, _, _ := in.readEnv()
				return hostStacksDirRules(ctx, filepath.Clean(*data), dirs, int(uid), env["DOCKER_HOST"])
			},
			in: in, units: &agentService{reqDir: reqDir, uid: uid}, settle: 2 * time.Second,
			restart: func(ctx context.Context) error { return agentSystemctl(ctx, "restart", agentUnitName) },
		}
		return a.run(ctx, req.Path)
	}

	res, err := rootupdate.Apply(ctx, rootupdate.Config{
		Installed:  version.Version,
		RequestDir: reqDir,
		ServiceUID: uid,
		StateDir:   managedStateDir,
		Asset:      managedArchive(),
		Files: []rootupdate.File{
			{Name: "dockmesh-agent-linux-" + runtime.GOARCH, Dest: binary, KeepPrev: true},
		},
		Verifier: releasesig.Default(),
		Log:      logger,
		Service:  &agentService{reqDir: reqDir, uid: uid},
	})
	if err != nil {
		return err
	}
	if res.Status != "" {
		logger.Info("update run finished", "status", res.Status, "from", res.From, "to", res.To, "step", res.Step, "reason", res.Reason)
	}
	if res.Status == rootupdate.StatusFailed {
		return errors.New("the rollback did not finish; it is retried on the next run")
	}
	return nil
}

// agentServiceUID is the account systemd runs the agent unit as, drop-ins
// included: root for an agent installed as root.
func agentServiceUID() (uint32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/systemctl", "show", "-p", "LoadState", "-p", "User", agentUnitName).Output() // #nosec G204 -- fixed unit name
	if err != nil {
		return 0, fmt.Errorf("ask systemd for the account of %s: %w", agentUnitName, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	// An unknown unit shows an empty User too, which must not mean root.
	if props["LoadState"] != "loaded" {
		return 0, fmt.Errorf("%s is not loaded (%q)", agentUnitName, props["LoadState"])
	}
	name := props["User"]
	if name == "" || name == "root" {
		return 0, nil
	}
	if name != agentAccount {
		return 0, fmt.Errorf("%s runs as %q, not %s or root", agentUnitName, name, agentAccount)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("look up %s: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(uid), nil
}

// agentService is the agent's side of rootupdate.Service. Only the binary
// is swapped and rolled back: certificates and enrollment state belong to
// the agent and are never put back.
type agentService struct {
	reqDir  string
	uid     uint32
	started time.Time
}

// agentUnits are the units a rollback restores.
var agentUnits = []string{agentUnitFile, managedPathUnit, managedServiceUnit}

func agentSystemctl(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/systemctl", args...).CombinedOutput() // #nosec G204 -- fixed unit names
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *agentService) Stop(ctx context.Context) error {
	return agentSystemctl(ctx, "stop", agentUnitName)
}

func (s *agentService) Start(ctx context.Context) error {
	// A health file from before this start must not count. The agent
	// account must not be able to keep a rollback from starting the agent,
	// so a failure only means the health check fails.
	if err := rootupdate.RemoveServiceFile(s.reqDir, s.uid, runningFile); err != nil {
		slog.Warn("remove agent health file", "err", err)
	}
	s.started = time.Now()
	return agentSystemctl(ctx, "start", agentUnitName)
}

func (s *agentService) DaemonReload(ctx context.Context) error {
	return agentSystemctl(ctx, "daemon-reload")
}

func (s *agentService) ReadUnit() ([]byte, error) {
	units := map[string][]byte{}
	for _, f := range agentUnits {
		data, err := readRootOwnedUnit(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		units[f] = data
	}
	return json.Marshal(units)
}

func (s *agentService) RestoreUnit(data []byte) error {
	var units map[string][]byte
	if err := json.Unmarshal(data, &units); err != nil {
		return err
	}
	var errs []error
	for _, f := range agentUnits {
		if content, ok := units[f]; ok {
			errs = append(errs, putUnit(f, content))
		}
	}
	return errors.Join(errs...)
}

// WriteUnit runs the new binary's `system install`, so the units come from
// the template of the version being installed and keep the host's values.
// agent.env is not rolled back with the units: every version must read the
// keys system install writes the same way.
func (s *agentService) WriteUnit(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "system", "install") // #nosec G204 -- the verified binary just installed
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s system install: %w: %s", binary, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *agentService) Snapshot(context.Context) error { return nil }

func (s *agentService) RestoreSnapshot(context.Context) error { return nil }

// Healthy is true once the agent started by this run connected to the
// server as want and wrote runningFile.
func (s *agentService) Healthy(_ context.Context, want string) bool {
	data, mod, err := rootupdate.ReadServiceFile(s.reqDir, s.uid, runningFile, 256)
	if err != nil || mod.Before(s.started) {
		return false
	}
	return strings.TrimPrefix(strings.TrimSpace(string(data)), "v") == strings.TrimPrefix(want, "v")
}

func readRootOwnedUnit(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- fixed unit paths
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

func putUnit(p string, data []byte) error {
	return agents.WriteFileAtomic(filepath.Dir(p), filepath.Base(p), bytes.NewReader(data), 0o644)
}

// legacyCopyStep reports a leftover of the legacy layout. Its root
// ExecStartPre copies a binary the agent account can write over
// pathBinary, so with it in place root would run whatever that account
// put there.
func legacyCopyStep(dataDir string) error {
	return legacyCopyStepAt(dataDir, legacyRefreshScript, agentUnitFile, false)
}

// legacyCopyStepAt with strict refuses when systemd can't be asked.
func legacyCopyStepAt(dataDir, script, unitFile string, strict bool) error {
	if err := legacyFilesLeft(dataDir, script); err != nil {
		return err
	}
	// Drop-ins and every spelling systemd accepts count, so ask systemd.
	pre, err := execStartPre()
	if err != nil && strict {
		return fmt.Errorf("ask systemd for the ExecStartPre of %s: %w", agentUnitName, err)
	}
	if pre != "" {
		return fmt.Errorf("%s runs ExecStartPre %s", agentUnitName, pre)
	}
	unit, err := readRootOwnedUnit(unitFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", unitFile, err)
	}
	for _, line := range strings.Split(string(unit), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStartPre=") || strings.Contains(line, "refresh-path-copy") {
			return fmt.Errorf("%s still has %q", unitFile, line)
		}
	}
	return nil
}

// legacyFilesLeft reports the copy script or the data dir binary of the
// legacy layout.
func legacyFilesLeft(dataDir, script string) error {
	for _, p := range []string{script, filepath.Join(dataDir, "bin", "dockmesh-agent")} {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%s still exists", p)
		}
	}
	return nil
}

// execStartPre is the ExecStartPre systemd has for the agent unit,
// drop-ins included; empty without one.
var execStartPre = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/systemctl", "show", "-p", "ExecStartPre", "--value", agentUnitName).Output() // #nosec G204 -- fixed unit name
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

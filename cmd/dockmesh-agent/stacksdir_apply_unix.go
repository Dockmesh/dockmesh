//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

// stacksDirResultMsg is root's report of a stacks-dir-apply.
type stacksDirResultMsg struct {
	Status   string                `json:"status"`
	Check    agents.StacksDirCheck `json:"check"`
	Reason   string                `json:"reason,omitempty"`
	Finished time.Time             `json:"finished"`
}

// requestOf returns the type and path of the request in reqDir, empty
// when there is none or it cannot be read; Apply then refuses it.
func requestOf(reqDir string, uid uint32) rootupdate.Request {
	var r rootupdate.Request
	data, _, err := rootupdate.ReadServiceFile(reqDir, uid, rootupdate.RequestFile, rootupdate.MaxRequestSize)
	if err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

// hostStacksDirRules collects the facts checkStacksDir needs on this host.
// The server's data dir comes from its config when this account may read
// it; the default is refused in any case.
func hostStacksDirRules(ctx context.Context, dataDir, current string, uid int, dockerHost string) stacksDirRules {
	servers := []string{defaultServerDataDir}
	if env, err := layout.ReadEnvFile(layout.ConfigFile); err == nil && filepath.IsAbs(env["DOCKMESH_DATA_DIR"]) {
		servers = append(servers, filepath.Clean(env["DOCKMESH_DATA_DIR"]))
	}
	return stacksDirRules{
		dataDir:        dataDir,
		current:        current,
		serverDataDirs: servers,
		dockerRoot:     dockerRootDir(ctx, dockerHost),
		uid:            uid,
		mountinfo:      procMountinfo,
	}
}

// dockerRootDir asks Docker for its data root, empty when it can't.
func dockerRootDir(ctx context.Context, host string) string {
	opts := []client.Opt{}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return ""
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil || !filepath.IsAbs(res.Info.DockerRootDir) {
		return ""
	}
	return filepath.Clean(res.Info.DockerRootDir)
}

// unitStore reads and puts back the agent's units.
type unitStore interface {
	ReadUnit() ([]byte, error)
	RestoreUnit(data []byte) error
}

// stacksDirApplier is root's side of a stacks-dir-apply. The request is
// untrusted: only its path is read, and every rule is checked again here.
type stacksDirApplier struct {
	dataDir  string
	reqDir   string
	uid      uint32
	gid      int
	stateDir string
	log      *slog.Logger
	// rules returns the host facts for the current stacks dir.
	rules func(current string) stacksDirRules
	in    *agentInstaller
	units unitStore
	// restart restarts the agent unit.
	restart func(ctx context.Context) error
	// settle gives the agent a moment to read the result before the
	// restart ends its connection.
	settle time.Duration
}

func (a *stacksDirApplier) result(status string, check agents.StacksDirCheck, err error) {
	m := stacksDirResultMsg{Status: status, Check: check, Finished: time.Now().UTC()}
	if err != nil {
		m.Reason = err.Error()
	}
	data, _ := json.Marshal(m)
	if werr := rootupdate.WriteServiceFile(a.reqDir, a.uid, rootupdate.StacksDirResultFile, data); werr != nil {
		a.log.Error("stacks-dir-apply result not written", "err", werr)
	}
	attrs := []any{"status", status, "path", check.Path, "issue", check.Issue}
	if err != nil {
		attrs = append(attrs, "reason", err.Error())
	}
	if status == agents.StacksDirDone {
		a.log.Info("stacks-dir-apply applied", attrs...)
	} else {
		a.log.Warn("stacks-dir-apply "+status, attrs...)
	}
}

func (a *stacksDirApplier) run(ctx context.Context, path string) error {
	check := agents.StacksDirCheck{Path: path}
	if err := rootupdate.RemoveServiceFile(a.reqDir, a.uid, rootupdate.RequestFile); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(a.reqDir, rootupdate.PendingFile)); err == nil || rootupdate.Unfinished(a.stateDir) {
		a.result(agents.StacksDirRefused, check, errors.New("an update is in flight"))
		return nil
	}
	unlock, err := lockStateFile(a.stateDir, "stacks-dir.lock")
	if err != nil {
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	defer unlock()

	envFile := a.in.envFile
	env, _, err := a.in.readEnv()
	if err != nil {
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	dirs := resolveStackDirs(env["DOCKMESH_STACKS_DIR"], env["DOCKMESH_EARLIER_STACKS_DIRS"], a.dataDir)
	res := checkStacksDir(path, a.rules(dirs.current))
	check = res.StacksDirCheck
	switch {
	case !res.ok():
		a.result(agents.StacksDirRefused, check, errors.New(res.Message))
		return nil
	case res.Issue == sdUnchanged:
		a.result(agents.StacksDirDone, check, nil)
		return nil
	}
	earlier, err := keptStackDirs(a.dataDir, path, dirs)
	if err != nil {
		a.result(agents.StacksDirRefused, check, err)
		return nil
	}

	prevEnv, err := os.ReadFile(envFile) // #nosec G304 -- root's agent.env
	if err != nil {
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	prevUnits, err := a.units.ReadUnit()
	if err != nil {
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	if res.WillCreate && !strings.HasPrefix(path, a.dataDir+"/") {
		if err := createStacksDir(path, res.existing, int(a.uid), a.gid, a.in.rootUID, a.in.rootGID); err != nil {
			a.result(agents.StacksDirFailed, check, fmt.Errorf("create %s: %w", path, err))
			return nil
		}
	}
	if _, err := a.in.run(installOptions{StacksDir: path, earlier: earlier, setEarlier: true}); err != nil { //nolint:contextcheck // short local file writes
		rerr := errors.Join(a.units.RestoreUnit(prevUnits), restoreEnv(envFile, prevEnv, a.in.rootUID))
		if rerr != nil {
			err = fmt.Errorf("%w; restoring the old config failed: %w", err, rerr)
		}
		a.result(agents.StacksDirRefused, check, err)
		return nil
	}
	if err := a.in.activate(true); err != nil {
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	a.result(agents.StacksDirDone, check, nil)
	sleepFor(ctx, a.settle)
	if err := a.restart(ctx); err != nil {
		a.result(agents.StacksDirFailed, check, err)
	}
	return nil
}

// keptStackDirs are the earlier stacks dirs after a switch to target: the
// current one and the earlier ones while they still hold stacks, at most
// maxEarlierStacksDirs outside the data dir.
func keptStackDirs(dataDir, target string, d stackDirs) ([]string, error) {
	var kept []string
	outside := 0
	for _, e := range d.all() {
		if e == target || containsString(kept, e) {
			continue
		}
		n, err := countStacks(e)
		if err != nil {
			return nil, fmt.Errorf("count the stacks in %s: %w", e, err)
		}
		if n == 0 {
			continue
		}
		kept = append(kept, e)
		if !strings.HasPrefix(e, dataDir+"/") {
			outside++
		}
	}
	if outside > maxEarlierStacksDirs {
		return nil, fmt.Errorf("%s still hold stacks; move or delete them first, the agent keeps at most %d earlier stacks directories", strings.Join(kept, ", "), maxEarlierStacksDirs)
	}
	return kept, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func restoreEnv(envFile string, data []byte, rootUID int) error {
	fi, err := os.Lstat(filepath.Dir(envFile))
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the config dir's group")
	}
	_, err = layout.WriteRootFile(envFile, data, rootUID, int(st.Gid), 0o640)
	return err
}

// createStacksDir creates the components of p from the existing-th on,
// each through the descriptor of the one before and without following a
// symlink: rootUID:rootGID 0755 in between, the last owned by uid:gid
// 0750.
func createStacksDir(p string, existing, uid, gid, rootUID, rootGID int) error {
	comps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	for i, c := range comps {
		last := i == len(comps)-1
		if i >= existing {
			mode := uint32(0o755)
			if last {
				mode = 0o750
			}
			if err := unix.Mkdirat(fd, c, mode); err != nil {
				return err
			}
		}
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		unix.Close(fd)
		fd = nfd
		if i >= existing {
			owner, group, mode := rootUID, rootGID, uint32(0o755)
			if last {
				owner, group, mode = uid, gid, 0o750
			}
			if err := unix.Fchown(fd, owner, group); err != nil {
				return err
			}
			if err := unix.Fchmod(fd, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// lockStateFile takes an exclusive lock on name in root's state dir, so
// only one stacks-dir-apply runs at a time.
func lockStateFile(stateDir, name string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, name), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- root's state dir
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another stacks-dir-apply is running")
	}
	return func() { f.Close() }, nil
}

func sleepFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// stacksDirWait is how long the agent waits for root's answer.
const stacksDirWait = 60 * time.Second

// applyStacksDir is the agent's side: it checks the path with root's rules
// first, so a refusal needs no root round trip, then asks the root updater
// to switch and waits for its answer. Root restarts the agent once done.
func applyStacksDir(ctx context.Context, req agents.StacksDirApplyReq) (agents.StacksDirApplyRes, error) {
	dirs := agentStackDirs()
	res := checkStacksDir(req.Path, hostStacksDirRules(ctx, dataDir(), dirs.current, os.Getuid(), os.Getenv("DOCKER_HOST")))
	out := agents.StacksDirApplyRes{Status: agents.StacksDirChecked, Check: res.StacksDirCheck}
	switch {
	case !res.ok():
		out.Status, out.Reason = agents.StacksDirRefused, res.Message
		return out, nil
	case req.CheckOnly:
		return out, nil
	case res.Issue == sdUnchanged:
		out.Status = agents.StacksDirDone
		return out, nil
	case agentLayout != agents.LayoutManaged:
		out.Status, out.Reason = agents.StacksDirRefused, "the stacks directory is changed by the root updater of a managed agent; this agent uses the legacy layout"
		return out, nil
	}
	if !upgradeMu.TryLock() {
		return out, errUpgradeBusy
	}
	defer upgradeMu.Unlock()
	dir := updateDir(dataDir())
	if err := os.Mkdir(dir, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return out, err
	}
	for _, f := range []string{rootupdate.RequestFile, rootupdate.PendingFile} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return out, errUpgradeBusy
		}
	}
	resultPath := filepath.Join(dir, rootupdate.StacksDirResultFile)
	if err := os.Remove(resultPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	body, _ := json.Marshal(rootupdate.Request{Type: rootupdate.RequestStacksDirApply, Path: req.Path})
	asked := time.Now()
	if err := writeStaged(dir, rootupdate.RequestFile, body); err != nil {
		return out, err
	}
	slog.Info("stacks dir: asked the root updater", "path", req.Path)
	deadline := time.NewTimer(stacksDirWait)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-deadline.C:
			out.Status = agents.StacksDirPending
			return out, nil
		case <-tick.C:
		}
		data, err := os.ReadFile(resultPath) // #nosec G304 -- fixed name in the agent's update dir
		if err != nil {
			continue
		}
		var m stacksDirResultMsg
		if json.Unmarshal(data, &m) != nil || m.Finished.Before(asked.Add(-time.Second)) {
			continue
		}
		_ = os.Remove(resultPath)
		out.Status, out.Check, out.Reason = m.Status, m.Check, m.Reason
		return out, nil
	}
}

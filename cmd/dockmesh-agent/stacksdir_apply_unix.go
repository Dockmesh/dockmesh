//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/stacksdir"
)

// stacksDirResultMsg is root's report of a stacks-dir-apply.
type stacksDirResultMsg struct {
	agents.StacksDirApplyRes
	Finished time.Time `json:"finished"`
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
func hostStacksDirRules(ctx context.Context, dataDir string, dirs stackDirs, uid int, dockerHost string) stacksDirRules {
	servers := []string{defaultServerDataDir}
	if env, err := layout.ReadEnvFile(layout.ConfigFile); err == nil && filepath.IsAbs(env["DOCKMESH_DATA_DIR"]) {
		servers = append(servers, filepath.Clean(env["DOCKMESH_DATA_DIR"]))
	}
	return stacksDirRules{
		dataDir:        dataDir,
		current:        dirs.current,
		stackDirs:      dirs.all(),
		serverDataDirs: servers,
		dockerRoots:    stacksdir.DockerRoots(ctx, dockerHost),
		uid:            uid,
		account:        accountName(uid),
		mountinfo:      stacksdir.ProcMountinfo,
	}
}

var accountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// accountName is the name of uid, the managed agent's account when it
// cannot be looked up.
func accountName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && accountPattern.MatchString(u.Username) {
		return u.Username
	}
	return agentAccount
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
	rules   func(dirs stackDirs) stacksDirRules
	in      *agentInstaller
	units   unitStore
	restart func(ctx context.Context) error
	// settle gives the agent a moment to read the result before the
	// restart ends its connection.
	settle time.Duration
}

func (a *stacksDirApplier) result(status string, check agents.StacksDirCheck, err error) {
	m := stacksDirResultMsg{StacksDirApplyRes: agents.StacksDirApplyRes{Status: status, Check: check}, Finished: time.Now().UTC()}
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
	unlock, err := lockStacksDir(a.stateDir)
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
	res := checkStacksDir(path, a.rules(dirs))
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
	created := false
	// A directory created so far is not deleted again; the result names it.
	failed := func(err error) error {
		rerr := errors.Join(a.units.RestoreUnit(prevUnits), restoreEnv(envFile, prevEnv, a.in.rootUID), a.in.systemctl("daemon-reload"))
		if rerr != nil {
			err = fmt.Errorf("%w; restoring the old config failed: %w", err, rerr)
		}
		if created {
			err = fmt.Errorf("%w; %s was created and left in place", err, path)
		}
		a.result(agents.StacksDirFailed, check, err)
		return nil
	}
	if res.WillCreate && !layout.Within(a.dataDir, path) {
		if err := stacksdir.Create(path, int(a.uid), a.gid); err != nil {
			return failed(fmt.Errorf("create %s: %w", path, err))
		}
		created = true
		if err := recordCreatedDir(a.stateDir, path); err != nil {
			a.log.Warn("stacks-dir-apply: record the new directory", "path", path, "err", err)
		}
	}
	if _, err := a.in.run(installOptions{StacksDir: path, earlier: earlier, setEarlier: true}); err != nil { //nolint:contextcheck // short local file writes
		return failed(err)
	}
	if err := a.in.activate(true); err != nil {
		return failed(err)
	}
	a.result(agents.StacksDirDone, check, nil)
	sleepCtx(ctx, a.settle)
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
		if e == target || slices.Contains(kept, e) {
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
		if !layout.Within(dataDir, e) {
			outside++
		}
	}
	if outside > maxEarlierStacksDirs {
		return nil, fmt.Errorf("%s still hold stacks; move or delete them first, the agent keeps at most %d earlier stacks directories", strings.Join(kept, ", "), maxEarlierStacksDirs)
	}
	return kept, nil
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

// lockStacksDir takes the lock in root's state dir that a stacks-dir-apply
// and `system install` hold while they rewrite agent.env and the unit.
func lockStacksDir(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "stacks-dir.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- root's state dir
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another change of the agent's config is running")
	}
	return func() { f.Close() }, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
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
	res := checkStacksDir(req.Path, hostStacksDirRules(ctx, dataDir(), dirs, os.Getuid(), os.Getenv("DOCKER_HOST")))
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
		return m.StacksDirApplyRes, nil
	}
}

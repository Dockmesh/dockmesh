//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/stacksdir"
	"github.com/dockmesh/dockmesh/internal/webtls"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// requestType returns the type of the pending request in reqDir, empty
// for an update or when there is none.
func requestType(reqDir string, uid uint32) string {
	data, _, err := rootupdate.ReadServiceFile(reqDir, uid, rootupdate.RequestFile, rootupdate.MaxRequestSize)
	if err != nil {
		return ""
	}
	var r rootupdate.Request
	if json.Unmarshal(data, &r) != nil {
		return ""
	}
	return r.Type
}

// setupApplier is root's side of a setup-apply: it commits the setup of a
// never set up server and refuses on any other.
type setupApplier struct {
	dataDir  string
	reqDir   string
	uid, gid uint32
	binary   string
	log      *slog.Logger
	now      func() time.Time
	// fromFile is a setup file applied from the command line: the values
	// come with the call, the result goes to report, and nobody polls.
	fromFile bool
	report   func(applyResult)
	// restoreFrom (and restoreKey for an encrypted backup) restore a
	// system backup instead of creating a new install.
	restoreFrom, restoreKey *os.File
	// restoreFromState restores the backup the wizard left in the state
	// dir. The restore opens it as the service account, so it reads only
	// what that account may read anyway.
	restoreFromState bool
}

// failedSuffix marks what a failed setup-apply leaves behind; it is never
// deleted, and nothing reads it as a config or database.
func (a *setupApplier) failedSuffix() string {
	return ".failed-" + a.now().UTC().Format("20060102T150405Z")
}

func (a *setupApplier) result(status, step string, err error) {
	r := applyResult{Status: status, Step: step}
	if err != nil {
		r.Reason = err.Error()
	}
	if a.report != nil {
		a.report(r)
		return
	}
	data, _ := json.Marshal(r)
	if werr := rootupdate.WriteServiceFile(a.reqDir, a.uid, setupResultFile, data); werr != nil {
		a.log.Error("setup-apply result not written", "err", werr)
	}
	if err != nil {
		a.log.Error("setup-apply "+status, "step", step, "err", err)
	} else {
		a.log.Info("setup-apply " + status)
	}
}

// run handles one setup-apply request. The request is removed first, so
// the path unit does not start the same one again.
func (a *setupApplier) run(ctx context.Context) error {
	if err := rootupdate.RemoveServiceFile(a.reqDir, a.uid, rootupdate.RequestFile); err != nil {
		return err
	}
	if mode := a.decide(); mode != setup.NeverSetUp {
		a.result(applyRefused, "decide", fmt.Errorf("this server is %s; setup-apply only runs on a server that was never set up", mode))
		return nil
	}
	v, err := a.values()
	if err != nil {
		a.result(applyRefused, "values", err)
		return nil
	}
	a.restoreFromState = v.Restore
	a.commit(ctx, v)
	return nil
}

// commit sets up a never set up server from v: target dir, config check,
// data, config, unit, restart and health, rolling back on any failure.
// Every outcome goes to a.result.
func (a *setupApplier) commit(ctx context.Context, v setup.ApplyValues) {
	unlock, err := lockSetup()
	if err != nil {
		a.result(applyFailed, "lock", err)
		return
	}
	defer unlock()
	// Another apply may have finished while this one waited for the lock.
	if mode := a.decide(); mode != setup.NeverSetUp {
		a.result(applyRefused, "decide", fmt.Errorf("this server is %s; setup-apply only runs on a server that was never set up", mode))
		return
	}
	if err := checkCertDirs(v); err != nil {
		a.result(applyRefused, "values", err)
		return
	}
	created, err := a.prepareTarget(v.DataDir)
	if err != nil {
		a.result(applyRefused, "data_dir", err)
		return
	}
	stacksCreated, err := a.prepareStacksDir(ctx, v)
	if err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyRefused, "stacks_dir", err)
		return
	}
	if err := checkProxyPorts(v); err != nil {
		dropTarget(v.DataDir, created)
		if stacksCreated {
			_ = os.Remove(v.StacksDir)
		}
		a.result(applyRefused, "proxy", err)
		return
	}
	applied := false
	defer func() {
		// A failed restore may have filled it: then it is set aside, so a
		// retry finds the path free again.
		if stacksCreated && !applied && os.Remove(v.StacksDir) != nil {
			if err := os.Rename(v.StacksDir, v.StacksDir+a.failedSuffix()); err != nil {
				a.log.Warn("set aside the failed stacks dir", "path", v.StacksDir, "err", err)
			}
		}
	}()
	envData, err := v.ConfigEnv(filepath.Join(filepath.Dir(filepath.Dir(a.binary)), "share", "dockmesh"))
	if err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyRefused, "config", err)
		return
	}
	gid := configGID()
	if err := layout.PrepareConfigDir(gid); err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyFailed, "config", err)
		return
	}
	candidate, err := writeRootConfig(layout.ConfigDir, ".dockmesh.env.new-", envData, gid)
	if err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyFailed, "config", err)
		return
	}
	defer os.Remove(candidate)
	if out, err := runAsService(ctx, a.binary, time.Minute, "serve", "--check-config", "--env-file", candidate); err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyRefused, "check_config", fmt.Errorf("%w %s", err, strings.TrimSpace(string(out))))
		return
	}
	// The service could read the pair itself, so root reads nothing new.
	if webtls.Mode(v.TLSMode) == webtls.File {
		if expired, end, err := setup.CertExpired(v.TLSCertFile, a.now()); err == nil && expired {
			dropTarget(v.DataDir, created)
			a.result(applyRefused, "check_config", fmt.Errorf("the certificate %s expired on %s", v.TLSCertFile, end.UTC().Format("2006-01-02")))
			return
		}
	}
	if step, err := a.populate(ctx, v, candidate); err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, step, err)
		return
	}

	svc := &serverService{binary: a.binary}
	prevUnit, err := svc.ReadUnit()
	if err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, "unit", err)
		return
	}
	if err := os.Rename(candidate, layout.ConfigFile); err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, "config", err)
		return
	}
	if _, err := writeServerUnit(""); err != nil { //nolint:contextcheck // the unit writer runs its own short-lived children
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "unit", err)
		return
	}
	a.result(applyRestarting, "restart", nil)
	if !a.fromFile {
		// The wizard polls the result every second; let it pass the
		// restart on before the server stops.
		time.Sleep(3 * time.Second)
	}
	if err := systemctl(ctx, "restart", serverUnitName); err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "start", err)
		return
	}
	env, err := layout.ReadEnvFile(layout.ConfigFile)
	if err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", err)
		return
	}
	paths := config.ResolvePaths(func(k string) string { return env[k] })
	deadline := time.Now().Add(rootupdate.DefaultHealthTimeout)
	health, client, err := healthProbe(env, paths)
	// Without setup mode's PKI (a setup file) the server issues its
	// certificate at the first start; the probe pins it once it exists.
	for errors.Is(err, fs.ErrNotExist) && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(2 * time.Second)
		health, client, err = healthProbe(env, paths)
	}
	if err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", err)
		return
	}
	svc.health, svc.client = health, client
	for !svc.Healthy(ctx, version.Version) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", errors.New("the server did not report healthy with the new config"))
			return
		}
		time.Sleep(2 * time.Second)
	}
	if err := os.RemoveAll(setup.StateDir(a.dataDir)); err != nil {
		a.log.Warn("setup state dir not removed", "err", err)
	}
	if a.fromFile {
		// A setup file runs before setup mode ever started the service.
		if err := systemctl(ctx, "enable", serverUnitName); err != nil {
			a.log.Warn("dockmesh.service not enabled", "err", err)
		}
	}
	applied = true
	a.result(applyApplied, "", nil)
}

// checkProxyPorts refuses the reverse proxy while another program listens
// on 80 or 443, which Caddy binds on the host. Root can bind them, so the
// check is exact.
func checkProxyPorts(v setup.ApplyValues) error {
	if !v.Proxy.Enabled {
		return nil
	}
	for _, p := range []int{80, 443} {
		if c := setup.CheckPort(p); c.Issue == setup.IssuePortBusy {
			return fmt.Errorf("port %d is in use by another program; free it or turn the reverse proxy off", p)
		}
	}
	return nil
}

// prepareStacksDir checks a stacks dir outside the data dir with root's
// view of the host and creates it for the service account when it is
// new. It reports whether it created one.
func (a *setupApplier) prepareStacksDir(ctx context.Context, v setup.ApplyValues) (bool, error) {
	if !v.StacksOutsideDataDir() {
		return false, nil
	}
	if a.uid > math.MaxInt32 || a.gid > math.MaxInt32 {
		return false, errors.New("the dockmesh account has an out-of-range uid or gid")
	}
	uid, gid := int(a.uid), int(a.gid)
	res := stacksdir.Check(v.StacksDir, stacksdir.ServerRules(v.DataDir, uid, layout.ServiceGroup, stacksdir.DockerRoots(ctx, "")))
	if !res.OK() {
		return false, errors.New(res.Message)
	}
	if !res.WillCreate {
		return false, nil
	}
	if err := stacksdir.Create(v.StacksDir, uid, gid); err != nil {
		return false, fmt.Errorf("create %s: %w", v.StacksDir, err)
	}
	return true, nil
}

// populate fills the new data dir as the service account: a restored
// backup, or a new database with the admin. It returns the step name for
// the result.
func (a *setupApplier) populate(ctx context.Context, v setup.ApplyValues, candidate string) (string, error) {
	switch {
	case a.restoreFromState:
		state := setup.StateDir(a.dataDir)
		args := []string{"restore", "--from", filepath.Join(state, setup.RestoreArchiveFile)}
		if _, err := os.Lstat(filepath.Join(state, setup.RestoreKeyFile)); err == nil {
			args = append(args, "--key", filepath.Join(state, setup.RestoreKeyFile))
		}
		out, err := runRestoreAsService(ctx, a.binary, 30*time.Minute, nil, nil, candidate, append(args, "--env-file", candidate)...)
		a.warnLostOwners(out)
		return "restore", err
	case a.restoreFrom != nil:
		args := []string{"restore", "--from", "-"}
		if a.fromFile && v.BaseURL != "" {
			args = append(args, "--base-url", v.BaseURL)
		}
		var extra []*os.File
		if a.restoreKey != nil {
			extra = []*os.File{a.restoreKey}
			args = append(args, "--key-fd", "3")
		}
		out, err := runRestoreAsService(ctx, a.binary, 30*time.Minute, a.restoreFrom, extra, candidate, append(args, "--env-file", candidate)...)
		a.warnLostOwners(out)
		return "restore", err
	case a.fromFile:
		data, err := json.Marshal(v)
		if err != nil {
			return "materialize", err
		}
		_, err = runAsServiceIO(ctx, a.binary, 5*time.Minute, bytes.NewReader(data), nil,
			"setup", "materialize", "--data-dir", v.DataDir, "--values", "-")
		return "materialize", err
	default:
		_, err := runAsService(ctx, a.binary, 5*time.Minute, "setup", "materialize",
			"--state-dir", setup.StateDir(a.dataDir), "--data-dir", v.DataDir)
		return "materialize", err
	}
}

// warnLostOwners passes on the restore's warning that stack data of other
// users now belongs to the service account.
func (a *setupApplier) warnLostOwners(out []byte) {
	for _, line := range strings.Split(string(out), "\n") {
		w, ok := strings.CutPrefix(strings.TrimSpace(line), lostOwnersWarning)
		if !ok {
			continue
		}
		n, _, _ := strings.Cut(w, " ")
		if entries, err := strconv.Atoi(n); err == nil {
			a.log.Warn("restore: Docker was unavailable, so stack data owned by other users now belongs to the dockmesh account", "entries", entries)
		}
	}
}

// decide runs the setup decision on this host itself; root never takes
// the service's word for it.
func (a *setupApplier) decide() setup.Mode {
	paths := config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" {
			return a.dataDir
		}
		return ""
	})
	facts := setup.Probe{ConfigFiles: []string{unitEnvFile()}, Paths: paths}.Scan()
	facts.Users = countUsersReadOnly(paths.DBPath, facts.DB)
	return setup.Decide(facts)
}

// values reads the wizard's values from the state dir, owned by the
// service account, through a checked handle.
func (a *setupApplier) values() (setup.ApplyValues, error) {
	data, _, err := rootupdate.ReadServiceFile(setup.StateDir(a.dataDir), a.uid, setup.ApplyFile, maxApplyFile)
	if err != nil {
		return setup.ApplyValues{}, err
	}
	return readApplyValues(strings.NewReader(string(data)))
}

// prepareTarget creates the new data dir for the service account, or
// takes over the server's own data dir or one the account owns already,
// when it holds nothing but what setup leaves. Every directory above it
// must belong to root and be writable by nobody else, so the account
// cannot swap a parent for a symlink between these steps.
func (a *setupApplier) prepareTarget(dir string) (created bool, err error) {
	exists, err := a.checkTarget(dir)
	if err != nil || exists {
		return false, err
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return false, err
	}
	if a.uid > math.MaxInt32 || a.gid > math.MaxInt32 {
		_ = os.Remove(dir)
		return false, errors.New("the dockmesh account has an out-of-range uid or gid")
	}
	if err := os.Lchown(dir, int(a.uid), int(a.gid)); err != nil {
		_ = os.Remove(dir)
		return false, err
	}
	return true, nil
}

// checkTarget is prepareTarget without changes: whether dir exists, and
// why it cannot become the data dir.
func (a *setupApplier) checkTarget(dir string) (exists bool, err error) {
	if err := setup.CheckDataDirPath(dir); err != nil {
		return false, err
	}
	if err := setup.RootOwnedAncestors(filepath.Dir(dir)); err != nil {
		return false, err
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !fi.IsDir():
		return true, fmt.Errorf("%s is not a directory", dir)
	}
	if owner, ok := setup.FileOwner(fi); !ok || owner != a.uid {
		return true, fmt.Errorf("%s exists and does not belong to the dockmesh account; choose a new directory", dir)
	}
	return true, a.onlySetupLeftovers(dir)
}

// checkCertDirs refuses certificate files whose directories another
// account could change: root reads them for its health check.
func checkCertDirs(v setup.ApplyValues) error {
	for _, f := range []string{v.TLSCertFile, v.TLSKeyFile} {
		if f != "" {
			if err := setup.RootOwnedAncestors(filepath.Dir(f)); err != nil {
				return fmt.Errorf("certificate file %s: %w", f, err)
			}
		}
	}
	return nil
}

// setupLockFile serializes setup-applies: the wizard's, run by the path
// unit, and one from a setup file.
const setupLockFile = "/run/dockmesh-setup.lock"

func lockSetup() (func(), error) {
	f, err := os.OpenFile(setupLockFile, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// onlySetupLeftovers accepts a data dir that is empty but for the state
// and update dirs of this setup, empty data and stacks dirs, and what an
// earlier failed apply set aside.
func (a *setupApplier) onlySetupLeftovers(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	same := filepath.Clean(dir) == filepath.Clean(a.dataDir)
	for _, e := range entries {
		name := e.Name()
		switch {
		case same && (name == setup.StateDirName || name == filepath.Base(layout.UpdateRequestDir(dir))):
		case failedName.MatchString(name) && (e.Type().IsRegular() || e.IsDir()):
		case (name == "data" || name == "stacks") && e.IsDir():
			if err := onlyFailed(filepath.Join(dir, name)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s is not empty (%s); choose an empty directory, or restore a backup to keep existing data", dir, name)
		}
	}
	return nil
}

// failedName is a file setAside or rollBack renamed.
var failedName = regexp.MustCompile(`^[A-Za-z0-9._-]+\.failed-[0-9]{8}T[0-9]{6}Z$`)

func onlyFailed(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !failedName.MatchString(e.Name()) || !e.Type().IsRegular() && !e.IsDir() {
			return fmt.Errorf("%s is not empty (%s)", dir, e.Name())
		}
	}
	return nil
}

// dropTarget removes a data dir this run created, if it is still empty.
func dropTarget(dir string, created bool) {
	if created {
		_ = os.Remove(dir)
	}
}

// setAside renames what a setup-apply created in the data dir, so the
// decision sees a never set up server again, and keeps it for a look.
// The PKI is still in the state dir, so its copies go too.
func (a *setupApplier) setAside(dir string) {
	paths := config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" {
			return dir
		}
		return ""
	})
	top, err := os.OpenRoot(dir)
	if err != nil {
		a.log.Warn("set aside the failed data", "err", err)
		return
	}
	defer top.Close()
	suffix := a.failedSuffix()
	a.setAsideIn(top, filepath.Base(filepath.Dir(paths.DBPath)), suffix)
	// A restore fills the stacks dir as well.
	if filepath.Dir(paths.StacksRoot) == filepath.Clean(dir) {
		stacks := filepath.Base(paths.StacksRoot)
		a.setAsideIn(top, stacks, suffix)
		if err := top.Rename(stacks+".restore-staged", stacks+".restore-staged"+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.log.Warn("set aside the failed data", "file", stacks+".restore-staged", "err", err)
		}
	}
}

// setAsideIn renames every entry of sub below top with suffix, so a retry
// finds the data dir as setup left it.
func (a *setupApplier) setAsideIn(top *os.Root, sub, suffix string) {
	// OpenRoot below top refuses a dir the account swapped for a symlink
	// out of the data dir.
	root, err := top.OpenRoot(sub)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.log.Warn("set aside the failed data", "err", err)
		}
		return
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		a.log.Warn("set aside the failed data", "err", err)
		return
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		a.log.Warn("set aside the failed data", "err", err)
	}
	for _, e := range entries {
		if n := e.Name(); !failedName.MatchString(n) {
			if err := root.Rename(n, n+suffix); err != nil {
				a.log.Warn("set aside the failed data", "file", n, "err", err)
			}
		}
	}
}

// rollBack puts the never set up server back after the new config was in
// place: config and data set aside, the old unit, setup mode started.
func (a *setupApplier) rollBack(ctx context.Context, svc *serverService, prevUnit []byte, dir, step string, cause error) {
	_ = svc.Stop(ctx)
	suffix := a.failedSuffix()
	var errs []error
	if err := os.Rename(layout.ConfigFile, layout.ConfigFile+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	errs = append(errs, svc.RestoreUnit(prevUnit), svc.DaemonReload(ctx),
		systemctl(ctx, "restart", filepath.Base(layout.UpdatePathUnitFile)))
	a.setAside(dir)
	// Quick retries can hit systemd's start limit; setup mode must still
	// come back.
	_ = systemctl(ctx, "reset-failed", serverUnitName)
	if a.fromFile {
		// A headless install never asked for setup mode; the service
		// stays stopped until the file is fixed and applied again.
		cause = fmt.Errorf("%w; dockmesh.service is left stopped", cause)
	} else {
		errs = append(errs, svc.Start(ctx))
	}
	if err := errors.Join(errs...); err != nil {
		cause = fmt.Errorf("%w; the rollback did not finish: %w", cause, err)
	}
	a.result(applyFailed, step, cause)
}

// writeRootConfig writes data to a new file root:gid 0640 in dir.
func writeRootConfig(dir, prefix string, data []byte, gid int) (string, error) {
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return "", err
	}
	name := f.Name()
	err = f.Chmod(0o640)
	if err == nil {
		err = f.Chown(0, gid)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

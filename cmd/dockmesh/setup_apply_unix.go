//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/setup"
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
	for _, f := range []string{v.TLSCertFile, v.TLSKeyFile} {
		// Root reads the certificate for its health check; the account
		// must not be able to point it elsewhere after the config check.
		if f != "" {
			if err := rootOwnedAncestors(filepath.Dir(f)); err != nil {
				a.result(applyRefused, "values", fmt.Errorf("certificate file %s: %w", f, err))
				return nil
			}
		}
	}
	created, err := a.prepareTarget(v.DataDir)
	if err != nil {
		a.result(applyRefused, "data_dir", err)
		return nil
	}
	envData, err := v.ConfigEnv(filepath.Join(filepath.Dir(filepath.Dir(a.binary)), "share", "dockmesh"))
	if err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyRefused, "config", err)
		return nil
	}
	gid := configGID()
	if err := layout.PrepareConfigDir(gid); err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyFailed, "config", err)
		return nil
	}
	candidate, err := writeRootConfig(layout.ConfigDir, ".dockmesh.env.new-", envData, gid)
	if err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyFailed, "config", err)
		return nil
	}
	defer os.Remove(candidate)
	if out, err := runAsService(ctx, a.binary, time.Minute, "serve", "--check-config", "--env-file", candidate); err != nil {
		dropTarget(v.DataDir, created)
		a.result(applyRefused, "check_config", fmt.Errorf("%w %s", err, strings.TrimSpace(string(out))))
		return nil
	}
	if _, err := runAsService(ctx, a.binary, 5*time.Minute, "setup", "materialize",
		"--state-dir", setup.StateDir(a.dataDir), "--data-dir", v.DataDir); err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, "materialize", err)
		return nil
	}

	svc := &serverService{binary: a.binary}
	prevUnit, err := svc.ReadUnit()
	if err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, "unit", err)
		return nil
	}
	if err := os.Rename(candidate, layout.ConfigFile); err != nil {
		a.setAside(v.DataDir)
		a.result(applyFailed, "config", err)
		return nil
	}
	if _, err := writeServerUnit(""); err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "unit", err)
		return nil
	}
	a.result(applyRestarting, "restart", nil)
	// The wizard polls the result every second; let it pass the restart
	// on before the server stops.
	time.Sleep(3 * time.Second)
	if err := systemctl(ctx, "restart", serverUnitName); err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "start", err)
		return nil
	}
	env, err := layout.ReadEnvFile(layout.ConfigFile)
	if err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", err)
		return nil
	}
	paths := config.ResolvePaths(func(k string) string { return env[k] })
	health, client, err := healthProbe(env, paths)
	if err != nil {
		a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", err)
		return nil
	}
	svc.health, svc.client = health, client
	deadline := time.Now().Add(rootupdate.DefaultHealthTimeout)
	for !svc.Healthy(ctx, version.Version) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			a.rollBack(ctx, svc, prevUnit, v.DataDir, "health", errors.New("the server did not report healthy with the new config"))
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	if err := os.RemoveAll(setup.StateDir(a.dataDir)); err != nil {
		a.log.Warn("setup state dir not removed", "err", err)
	}
	a.result(applyApplied, "", nil)
	return nil
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
	if err := setup.CheckDataDirPath(dir); err != nil {
		return false, err
	}
	if err := rootOwnedAncestors(filepath.Dir(dir)); err != nil {
		return false, err
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o750); err != nil {
			return false, err
		}
		if err := os.Lchown(dir, int(a.uid), int(a.gid)); err != nil {
			_ = os.Remove(dir)
			return false, err
		}
		return true, nil
	case err != nil:
		return false, err
	case !fi.IsDir():
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	if owner, ok := fileOwner(fi); !ok || owner != a.uid {
		return false, fmt.Errorf("%s exists and does not belong to the dockmesh account; choose a new directory", dir)
	}
	return false, a.onlySetupLeftovers(dir)
}

// rootOwnedAncestors checks dir and every directory above it: a real
// directory, owned by root, writable by nobody else.
func rootOwnedAncestors(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not absolute", dir)
	}
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		owner, ok := fileOwner(fi)
		if !fi.IsDir() || !ok || owner != 0 || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s must be a directory owned by root that only root may write", p)
		}
		if p == "/" {
			return nil
		}
	}
}

func fileOwner(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
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
		case failedName.MatchString(name) && e.Type().IsRegular():
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
		if !failedName.MatchString(e.Name()) || !(e.Type().IsRegular() || e.IsDir()) {
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
	// OpenRoot below top refuses a data dir the account swapped for a
	// symlink out of the data dir.
	root, err := top.OpenRoot(filepath.Base(filepath.Dir(paths.DBPath)))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.log.Warn("set aside the failed data", "err", err)
		}
		return
	}
	defer root.Close()
	suffix := a.failedSuffix()
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
	// Everything the failed start wrote goes, so a retry finds the data
	// dir as setup left it.
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
	errs = append(errs, svc.Start(ctx))
	if err := errors.Join(errs...); err != nil {
		cause = fmt.Errorf("%w; the rollback did not finish: %v", cause, err)
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

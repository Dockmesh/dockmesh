//go:build unix

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// applySetupAsRoot commits a setup file through root's setup-apply, the
// wizard's own commit: the same decision, checks, config, unit and
// rollback, with the values from the file instead of the state dir.
func applySetupAsRoot(ctx context.Context, run *setupFileRun, dryRun bool) (appliedSetup, error) {
	uid, gid, err := serviceAccount()
	if err != nil {
		return appliedSetup{}, fmt.Errorf("%w (the installer creates it; DOCKMESH_SETUP_FILE=<file> applies the file in the same run)", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return appliedSetup{}, err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return appliedSetup{}, err
	}
	if err := checkUnitBinary(binary); err != nil {
		return appliedSetup{}, err
	}
	dataDir := config.ResolvePaths(func(string) string { return "" }).DataDir
	var last applyResult
	a := &setupApplier{dataDir: dataDir, reqDir: layout.UpdateRequestDir(dataDir), uid: uid, gid: gid, binary: binary,
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)), now: time.Now,
		fromFile: true, report: func(r applyResult) { last = r }}
	if mode := a.decide(); mode != setup.NeverSetUp {
		return appliedSetup{}, notNeverSetUp(mode)
	}
	v := run.values
	if r := run.file.Restore; r != nil {
		backup, err := openOperatorFile(r.BackupFile)
		if err != nil {
			return appliedSetup{}, err
		}
		defer backup.Close()
		a.restoreFrom = backup
		if r.KeyFile != "" {
			key, err := openOperatorFile(r.KeyFile)
			if err != nil {
				return appliedSetup{}, err
			}
			defer key.Close()
			a.restoreKey = key
		}
	}
	if dryRun {
		return appliedSetup{}, a.check(v)
	}
	a.commit(ctx, v)
	if last.Status != applyApplied {
		msg := last.Reason
		if msg == "" {
			msg = last.Status
		}
		return appliedSetup{}, fmt.Errorf("setup %s at step %s: %s", last.Status, last.Step, msg)
	}
	url, fp := appliedURL(v, filepath.Join(v.DataDir, "data"))
	res := appliedSetup{URL: url, Fingerprint: fp}
	if run.file.Admin != nil {
		res.Admin = v.Admin.Username
	}
	return res, nil
}

// check is what commit checks before it changes anything, for --dry-run.
func (a *setupApplier) check(v setup.ApplyValues) error {
	if err := checkCertDirs(v); err != nil {
		return err
	}
	_, err := a.checkTarget(v.DataDir)
	return err
}

// openOperatorFile opens a file the operator named in a setup file: a
// regular file, never through a symlink, without blocking on a FIFO. As
// root it must belong to root and, like every directory above it, be
// writable by nobody else, so another account cannot swap in what root
// reads or hands on to the service account.
func openOperatorFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- named by the operator's setup file
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err == nil && os.Geteuid() == 0 {
		if owner, ok := fileOwner(fi); !ok || owner != 0 || fi.Mode().Perm()&0o022 != 0 {
			err = fmt.Errorf("%s must belong to root and be writable only by root", path)
		} else {
			err = rootOwnedAncestors(filepath.Dir(path))
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

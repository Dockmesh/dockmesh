//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/layout"
)

// maxUnitPathsOutput bounds what is read from the child; a real answer
// is a few hundred bytes.
const maxUnitPathsOutput = 64 << 10

// settingsWritablePaths returns the directories the server's settings
// let it write: the allowed local backup roots (their defaults while the
// setting was never written), the audit archive dir and the directory a
// local target without a path means. The database is read by a child
// running as the dockmesh account in its own session, so root never
// parses a file that account can rewrite, and every SQLite side file it
// creates belongs to that account.
func settingsWritablePaths(dbPath string) ([]string, error) {
	dataDir := filepath.Dir(dbPath)
	if _, err := os.Lstat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return targets.DefaultRoots(dataDir), nil
	} else if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := runAsService(context.Background(), self, 30*time.Second, "system", unitPathsCmd, "--db", dbPath)
	if err != nil {
		return nil, err
	}
	var out unitPathsOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if !out.RootsSet {
		out.Paths = append(out.Paths, targets.DefaultRoots(dataDir)...)
	}
	return out.Paths, nil
}

// serviceAccount returns the uid and gid of the dockmesh account, refusing
// root.
func serviceAccount() (uint32, uint32, error) {
	acct, err := user.Lookup(layout.ServiceGroup)
	if err != nil {
		return 0, 0, fmt.Errorf("look up the dockmesh account: %w", err)
	}
	uid, uidErr := strconv.ParseUint(acct.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(acct.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("the dockmesh account has uid %s, gid %s; refusing to act on its files as root", acct.Uid, acct.Gid)
	}
	return uint32(uid), uint32(gid), nil
}

// runAsService runs binary with args as the dockmesh account in its own
// session, with a minimal environment, and returns at most
// maxUnitPathsOutput bytes of its stdout. Root uses it for anything that
// parses or rewrites files that account owns.
func runAsService(ctx context.Context, binary string, timeout time.Duration, args ...string) ([]byte, error) {
	return runAsServiceIO(ctx, binary, timeout, nil, nil, args...)
}

// runAsServiceIO is runAsService with stdin and extra open files (fd 3
// onward), for files root may open but the service account may not.
func runAsServiceIO(ctx context.Context, binary string, timeout time.Duration, stdin io.Reader, extra []*os.File, args ...string) ([]byte, error) {
	uid, gid, err := serviceAccount()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdin = stdin
	cmd.ExtraFiles = extra
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:     true,
		Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}},
	}
	// The child's account can hold its pipes open; the deadline must
	// still end the call.
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, left: maxUnitPathsOutput}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxUnitPathsOutput+1))
	if readErr == nil && len(data) > maxUnitPathsOutput {
		readErr = errors.New("answer too large")
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return data, readErr
}

// limitedBuffer keeps the first left bytes written to it.
type limitedBuffer struct {
	buf  *bytes.Buffer
	left int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if n := min(len(p), l.left); n > 0 {
		l.buf.Write(p[:n])
		l.left -= n
	}
	return len(p), nil
}

// checkUnitBinary refuses a binary the unit would run as the service
// unless root owns it and every directory above it and nobody else may
// write any of them; otherwise whoever may replace it runs code as the
// service on the next restart.
func checkUnitBinary(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s must be owned by root and writable only by root to be run by the unit; install the binary with install.sh or to /usr/local/bin", p)
		}
		if p == "/" {
			return nil
		}
	}
}

// systemSnapshotDB is `dockmesh system snapshot-db`: install.sh runs it
// with the server stopped, before it replaces the binary, and gets the
// same copy the root updater takes. The copy is made by a child running
// as the dockmesh account, which owns the database.
func systemSnapshotDB() error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}
	// Without a config the server runs on the default paths.
	env := map[string]string{}
	envFile, err := unitConfigFile("")
	switch {
	case errors.Is(err, errNoConfig):
	case err != nil:
		return err
	default:
		if env, err = layout.ReadEnvFile(envFile); err != nil {
			return err
		}
	}
	dbPath := config.ResolvePaths(func(k string) string { return env[k] }).DBPath
	if _, err := os.Lstat(dbPath); errors.Is(err, fs.ErrNotExist) {
		fmt.Println("no database yet")
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := runAsService(context.Background(), self, dbStepTimeout, "update", updateSnapshotCmd, "--db", dbPath); err != nil {
		return err
	}
	fmt.Printf("snapshot %s\n", db.UpdateSnapshotPath(dbPath))
	return nil
}

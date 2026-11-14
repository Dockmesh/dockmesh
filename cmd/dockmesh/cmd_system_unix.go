//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// settingsWritablePaths returns the directories the server's settings
// let it write: the allowed local backup roots (their defaults while the
// setting was never written), the audit archive dir and the directory a
// local target without a path means. The database is read by a child
// running as the database's owner, so root never opens a file the
// service account can rewrite, and every SQLite side file it creates
// belongs to that account.
func settingsWritablePaths(dbPath string) ([]string, error) {
	dataDir := filepath.Dir(dbPath)
	fi, err := os.Lstat(dbPath)
	if errors.Is(err, fs.ErrNotExist) {
		return targets.DefaultRoots(dataDir), nil
	}
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok {
		return nil, fmt.Errorf("%s is not a regular file", dbPath)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "system", unitPathsCmd, "--db", dbPath)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, Groups: []uint32{}}}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out unitPathsOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, err
	}
	if !out.RootsSet {
		out.Paths = append(out.Paths, targets.DefaultRoots(dataDir)...)
	}
	return out.Paths, nil
}

// Package selinux resets the SELinux label of a file root put in place.
// A file renamed into place keeps the label of the directory it was
// created in, which may be one systemd is not allowed to execute.
package selinux

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// restorecon is looked up only at these paths, never through PATH: the
// caller runs as root.
var restoreconPaths = []string{"/usr/sbin/restorecon", "/sbin/restorecon"}

// enforceFile exists while selinuxfs is mounted, enforcing or permissive.
const enforceFile = "/sys/fs/selinux/enforce"

type relabeler struct {
	enabled func() bool
	stat    func(string) (os.FileInfo, error)
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
}

var std = relabeler{
	enabled: func() bool {
		_, err := os.Stat(enforceFile)
		return err == nil
	},
	stat: os.Stat,
	run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- fixed restorecon path, paths from the caller
	},
}

// Restore resets the label of each path to the policy default. Without
// SELinux or without restorecon it does nothing; a failure is logged.
func Restore(log *slog.Logger, paths ...string) {
	std.restore(log, paths...)
}

func (r relabeler) restore(log *slog.Logger, paths ...string) {
	if len(paths) == 0 || !r.enabled() {
		return
	}
	bin := ""
	for _, p := range restoreconPaths {
		if fi, err := r.stat(p); err == nil && fi.Mode().IsRegular() {
			bin = p
			break
		}
	}
	if bin == "" {
		log.Warn("SELinux is enabled but restorecon was not found; labels left as they are", "paths", paths)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append([]string{"--"}, paths...)
	if out, err := r.run(ctx, bin, args...); err != nil {
		log.Warn("reset SELinux label", "paths", paths, "err", err, "output", strings.TrimSpace(string(out)))
	}
}

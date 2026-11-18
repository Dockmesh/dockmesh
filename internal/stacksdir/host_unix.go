//go:build unix

package stacksdir

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

// DockerRoots asks the daemon at the default socket and the one
// DOCKER_HOST names for their data roots. Both count: a daemon the service
// is pointed at may not be the one that owns the host's Docker data.
func DockerRoots(ctx context.Context, host string) []string {
	var out []string
	for _, h := range []string{"", host} {
		if h == "" && len(out) > 0 {
			continue
		}
		if d := dockerRootDir(ctx, h); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// dockerRootDir asks one daemon, the default one for an empty host, for
// its data root; empty when it can't.
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

// Create creates p, whose parent exists, owned by uid:gid 0750.
// Every component is opened through the descriptor of the one before
// without following a symlink.
func Create(p string, uid, gid int) error {
	comps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	for i, c := range comps {
		if i == len(comps)-1 {
			if err := unix.Mkdirat(fd, c, 0o750); err != nil {
				return err
			}
		}
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		unix.Close(fd)
		fd = nfd
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return err
	}
	return unix.Fchmod(fd, 0o750)
}

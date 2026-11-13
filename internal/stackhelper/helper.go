// Package stackhelper works on stack directories that hold data owned by
// other users, such as a database bind directory created by the container
// as uid 999 with mode 0700. The Dockmesh services run unprivileged and
// cannot read or remove such files themselves, so a short-lived helper
// container does it as root. Only the stack directory is mounted into the
// helper, which keeps every operation inside it.
package stackhelper

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Image is the helper image, the same pinned busybox the other helpers use,
// so an air-gapped host needs only the one image it already pre-pulls.
const Image = docker.BusyboxHelperImage

// mountPoint is where the stack directory appears inside the helper.
const mountPoint = "/stack"

// API is the part of the Docker client the helper uses.
type API interface {
	client.ImageAPIClient
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	docker.HelperAPI
}

// Helper runs the helper container. A nil *Helper is valid and means no
// Docker is available: RemoveAll then only does the direct delete.
type Helper struct {
	cli API
}

// New returns a helper that talks to cli.
func New(cli API) *Helper {
	return &Helper{cli: cli}
}

// ErrOutside is returned for a target that is not a directory strictly
// below its base after symlinks are resolved.
var ErrOutside = errors.New("path is not a directory inside the stacks directory")

// resolve returns the real path of dir. dir itself must be a directory,
// not a symlink, and its real path must lie strictly below the real path
// of base. Only the result of resolve is ever mounted into a helper.
func resolve(base, dir string) (string, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: %w", dir, ErrOutside)
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realBase, realDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: %w", dir, ErrOutside)
	}
	return realDir, nil
}

// RemoveAll deletes dir, which must lie below base. It tries a normal
// delete first. Only when that fails for lack of permission does it empty
// the directory through the helper and then remove the empty directory
// itself. Either way the directory must be gone afterwards; otherwise the
// error lists what is left.
func (h *Helper) RemoveAll(ctx context.Context, base, dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if h == nil || h.cli == nil {
		return fmt.Errorf("%w (files owned by another user need Docker to remove them)", err)
	}
	real, rerr := resolve(base, dir)
	if rerr != nil {
		return rerr
	}
	if herr := h.empty(ctx, real); herr != nil {
		return fmt.Errorf("remove %s: %w%s", dir, herr, leftovers(dir))
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w%s", dir, err, leftovers(dir))
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: directory still exists%s", dir, leftovers(dir))
	}
	return nil
}

// leftovers names up to ten top-level entries still in dir, for an error
// message that tells the user exactly what was not removed.
func leftovers(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	more := ""
	if len(names) > 10 {
		more = fmt.Sprintf(" and %d more", len(names)-10)
		names = names[:10]
	}
	return "; left behind: " + strings.Join(names, ", ") + more
}

// emptyScript removes everything below the mount point, dotfiles
// included. Patterns that match nothing stay literal and rm -f ignores
// them.
const emptyScript = "rm -rf " + mountPoint + "/* " + mountPoint + "/.[!.]* " + mountPoint + "/..?*"

func (h *Helper) empty(ctx context.Context, real string) error {
	id, err := h.create(ctx, real, []string{"sh", "-c", emptyScript},
		// rm needs to enter and unlink in directories of other users,
		// and FOWNER covers sticky directories.
		[]string{"DAC_OVERRIDE", "FOWNER"})
	if err != nil {
		return err
	}
	defer h.remove(ctx, id)
	if _, err := h.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start helper: %w", err)
	}
	// A large data directory can take longer than docker.FinishHelper's
	// fixed deadline, so the wait follows ctx; remove kills it if ctx ends.
	wait := h.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case s := <-wait.Result:
		if s.StatusCode != 0 {
			return fmt.Errorf("helper exited with status %d", s.StatusCode)
		}
		return nil
	case err := <-wait.Error:
		return fmt.Errorf("helper wait: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Helper) create(ctx context.Context, real string, cmd, caps []string) (string, error) {
	if err := h.ensureImage(ctx); err != nil {
		return "", err
	}
	resp, err := h.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  Image,
			Cmd:    cmd,
			User:   "0:0",
			Labels: map[string]string{"dockmesh.managed": "true", "dockmesh.component": "stack-files-helper"},
		},
		HostConfig: &container.HostConfig{
			// A typed bind mount fails on a missing source instead of
			// creating it as root the way a -v bind would.
			Mounts:         []mount.Mount{{Type: mount.TypeBind, Source: real, Target: mountPoint}},
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			CapAdd:         caps,
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			LogConfig:      docker.HelperLogConfig,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create helper: %w", err)
	}
	return resp.ID, nil
}

func (h *Helper) remove(ctx context.Context, id string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.HelperTimeout)
	defer cancel()
	_, _ = h.cli.ContainerRemove(rctx, id, client.ContainerRemoveOptions{Force: true})
}

// ensureImage pulls the pinned image when it is missing. On an air-gapped
// host the error names the image, so the user knows what to load.
func (h *Helper) ensureImage(ctx context.Context) error {
	if _, err := h.cli.ImageInspect(ctx, Image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}
	if err := docker.PullAnonymous(ctx, h.cli, Image); err != nil {
		return fmt.Errorf("pull %s: %w", Image, err)
	}
	return nil
}

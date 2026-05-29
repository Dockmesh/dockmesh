package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

func (c *Client) ListContainers(ctx context.Context, all bool) ([]types.Container, error) {
	return c.cli.ContainerList(ctx, container.ListOptions{All: all})
}

func (c *Client) InspectContainer(ctx context.Context, id string) (types.ContainerJSON, error) {
	return c.cli.ContainerInspect(ctx, id)
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (c *Client) StopContainer(ctx context.Context, id string) error {
	return c.cli.ContainerStop(ctx, id, container.StopOptions{})
}

func (c *Client) RestartContainer(ctx context.Context, id string) error {
	return c.cli.ContainerRestart(ctx, id, container.StopOptions{})
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	return c.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

// PauseContainer freezes all processes in the container (SIGSTOP-equivalent
// via the freezer cgroup). Data in memory is preserved. Used for incident
// response when you want to inspect a misbehaving container without
// killing it.
func (c *Client) PauseContainer(ctx context.Context, id string) error {
	return c.cli.ContainerPause(ctx, id)
}

// UnpauseContainer resumes a paused container.
func (c *Client) UnpauseContainer(ctx context.Context, id string) error {
	return c.cli.ContainerUnpause(ctx, id)
}

// KillContainer sends a signal to the container's main process. Empty
// signal defaults to SIGKILL (Docker's default). Accepts the usual
// "SIGKILL", "SIGTERM", "SIGHUP" names or numeric strings like "9".
func (c *Client) KillContainer(ctx context.Context, id, signal string) error {
	return c.cli.ContainerKill(ctx, id, signal)
}

// ContainerStatPath stats a path inside the container without copying
// any bytes — used by the file-browser to decide "is this a dir or a
// file?" before deciding whether to stream a single file or a listing.
func (c *Client) ContainerStatPath(ctx context.Context, id, path string) (types.ContainerPathStat, error) {
	return c.cli.ContainerStatPath(ctx, id, path)
}

// CopyFromContainer streams the requested path out of the container as
// a tar archive. The returned ReadCloser must be drained and closed by
// the caller. Used by the file-browser for both directory listings
// (depth-1 tar walk) and file downloads (single tar entry → raw bytes).
func (c *Client) CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, types.ContainerPathStat, error) {
	return c.cli.CopyFromContainer(ctx, id, path)
}

// CopyToContainer writes a tar archive into the container at dstPath.
// Caller supplies the tar stream — usually a single-entry tar built in
// memory by the file-browser's upload handler.
func (c *Client) CopyToContainer(ctx context.Context, id, dstPath string, src io.Reader) error {
	return c.cli.CopyToContainer(ctx, id, dstPath, src, types.CopyToContainerOptions{})
}

package docker

import (
	"context"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func (c *Client) ListContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	res, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: all})
	return res.Items, err
}

func (c *Client) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	return res.Container, err
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (c *Client) StopContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	return err
}

func (c *Client) RestartContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return err
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	_, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force})
	return err
}

// PauseContainer freezes all processes in the container (SIGSTOP-equivalent
// via the freezer cgroup). Data in memory is preserved. Used for incident
// response when you want to inspect a misbehaving container without
// killing it.
func (c *Client) PauseContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return err
}

// UnpauseContainer resumes a paused container.
func (c *Client) UnpauseContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return err
}

// KillContainer sends a signal to the container's main process. Empty
// signal defaults to SIGKILL (Docker's default). Accepts the usual
// "SIGKILL", "SIGTERM", "SIGHUP" names or numeric strings like "9".
func (c *Client) KillContainer(ctx context.Context, id, signal string) error {
	_, err := c.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: signal})
	return err
}

// ContainerStatPath stats a path inside the container without copying
// any bytes — used by the file-browser to decide "is this a dir or a
// file?" before deciding whether to stream a single file or a listing.
func (c *Client) ContainerStatPath(ctx context.Context, id, path string) (container.PathStat, error) {
	res, err := c.cli.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: path})
	return res.Stat, err
}

// CopyFromContainer streams the requested path out of the container as
// a tar archive. The returned ReadCloser must be drained and closed by
// the caller. Used by the file-browser for both directory listings
// (depth-1 tar walk) and file downloads (single tar entry → raw bytes).
func (c *Client) CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, container.PathStat, error) {
	res, err := c.cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: path})
	return res.Content, res.Stat, err
}

// CopyToContainer writes a tar archive into the container at dstPath.
// Caller supplies the tar stream — usually a single-entry tar built in
// memory by the file-browser's upload handler.
func (c *Client) CopyToContainer(ctx context.Context, id, dstPath string, src io.Reader) error {
	_, err := c.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: dstPath, Content: src})
	return err
}

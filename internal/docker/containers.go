package docker

import (
	"context"

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

package docker

import (
	"context"

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

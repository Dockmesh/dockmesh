package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
)

// ContainerLogs returns a multiplexed stream of stdout+stderr.
// The caller must close the reader.
func (c *Client) ContainerLogs(ctx context.Context, id string, tail string, follow bool) (io.ReadCloser, error) {
	if tail == "" {
		tail = "100"
	}
	return c.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
}

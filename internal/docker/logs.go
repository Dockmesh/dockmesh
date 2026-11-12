package docker

import (
	"context"
	"io"

	"github.com/moby/moby/client"
)

// ContainerLogs returns a multiplexed stream of stdout+stderr.
// The caller must close the reader.
func (c *Client) ContainerLogs(ctx context.Context, id string, tail string, follow bool) (io.ReadCloser, error) {
	if tail == "" {
		tail = "100"
	}
	return c.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
}

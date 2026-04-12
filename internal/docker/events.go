package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
)

// Events subscribes to the Docker event stream. Returns two channels:
// one for events and one for errors. Both close when the context is cancelled.
func (c *Client) Events(ctx context.Context) (<-chan events.Message, <-chan error) {
	return c.cli.Events(ctx, types.EventsOptions{})
}

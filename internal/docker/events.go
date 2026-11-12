package docker

import (
	"context"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// Events subscribes to the Docker event stream. Returns two channels:
// one for events and one for errors. Both close when the context is cancelled.
func (c *Client) Events(ctx context.Context) (<-chan events.Message, <-chan error) {
	res := c.cli.Events(ctx, client.EventsListOptions{})
	return res.Messages, res.Err
}

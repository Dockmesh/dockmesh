package docker

import (
	"context"

	"github.com/docker/docker/api/types"
)

func (c *Client) ListNetworks(ctx context.Context) ([]types.NetworkResource, error) {
	return c.cli.NetworkList(ctx, types.NetworkListOptions{})
}

func (c *Client) InspectNetwork(ctx context.Context, id string) (types.NetworkResource, error) {
	return c.cli.NetworkInspect(ctx, id, types.NetworkInspectOptions{Verbose: true})
}

func (c *Client) CreateNetwork(ctx context.Context, name, driver string, labels map[string]string) (types.NetworkCreateResponse, error) {
	if driver == "" {
		driver = "bridge"
	}
	return c.cli.NetworkCreate(ctx, name, types.NetworkCreate{
		Driver: driver,
		Labels: labels,
	})
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	return c.cli.NetworkRemove(ctx, id)
}

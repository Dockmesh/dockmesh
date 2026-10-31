package docker

import (
	"context"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

func (c *Client) ListNetworks(ctx context.Context) ([]network.Inspect, error) {
	return c.cli.NetworkList(ctx, network.ListOptions{})
}

func (c *Client) InspectNetwork(ctx context.Context, id string) (network.Inspect, error) {
	return c.cli.NetworkInspect(ctx, id, network.InspectOptions{Verbose: true})
}

func (c *Client) CreateNetwork(ctx context.Context, name, driver string, labels map[string]string) (network.CreateResponse, error) {
	if driver == "" {
		driver = "bridge"
	}
	return c.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: driver,
		Labels: labels,
	})
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	return c.cli.NetworkRemove(ctx, id)
}

func (c *Client) PruneNetworks(ctx context.Context) (network.PruneReport, error) {
	return c.cli.NetworksPrune(ctx, filters.Args{})
}

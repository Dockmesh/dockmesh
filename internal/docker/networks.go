package docker

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ListNetworks returns the list as network.Inspect, the shape the API and
// the agent protocol carry. Containers starts empty, as in the daemon's
// own list response; handlers fill it from the container list.
func (c *Client) ListNetworks(ctx context.Context) ([]network.Inspect, error) {
	return ListNetworks(ctx, c.cli)
}

// ListNetworks is Client.ListNetworks for a bare SDK client.
func ListNetworks(ctx context.Context, cli client.NetworkAPIClient) ([]network.Inspect, error) {
	res, err := cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]network.Inspect, 0, len(res.Items))
	for _, n := range res.Items {
		out = append(out, network.Inspect{Network: n.Network, Containers: map[string]network.EndpointResource{}})
	}
	return out, nil
}

func (c *Client) InspectNetwork(ctx context.Context, id string) (network.Inspect, error) {
	res, err := c.cli.NetworkInspect(ctx, id, client.NetworkInspectOptions{Verbose: true})
	return res.Network, err
}

func (c *Client) CreateNetwork(ctx context.Context, name, driver string, labels map[string]string) (network.CreateResponse, error) {
	return CreateNetwork(ctx, c.cli, name, driver, labels)
}

// CreateNetwork is Client.CreateNetwork for a bare SDK client; the
// response keeps the daemon's single Warning string.
func CreateNetwork(ctx context.Context, cli client.NetworkAPIClient, name, driver string, labels map[string]string) (network.CreateResponse, error) {
	if driver == "" {
		driver = "bridge"
	}
	res, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: driver,
		Labels: labels,
	})
	if err != nil {
		return network.CreateResponse{}, err
	}
	return network.CreateResponse{ID: res.ID, Warning: strings.Join(res.Warning, "\n")}, nil
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	_, err := c.cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	return err
}

func (c *Client) PruneNetworks(ctx context.Context) (network.PruneReport, error) {
	res, err := c.cli.NetworkPrune(ctx, client.NetworkPruneOptions{})
	return res.Report, err
}

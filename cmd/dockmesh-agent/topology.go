package main

import (
	"context"

	dockerwrap "github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/topology"
	"github.com/moby/moby/client"
)

type topologyClient interface {
	client.NetworkAPIClient
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
}

// buildTopology answers req.networks.topology. It only lists, so the
// frame stays read-only whatever the server sends.
func buildTopology(ctx context.Context, cli topologyClient) (topology.Graph, error) {
	nets, err := dockerwrap.ListNetworks(ctx, cli)
	if err != nil {
		return topology.Graph{}, err
	}
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return topology.Graph{}, err
	}
	return topology.Build(nets, list.Items), nil
}

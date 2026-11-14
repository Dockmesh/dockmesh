package main

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type fakeTopologyClient struct {
	client.NetworkAPIClient
	listedAll bool
}

func (f *fakeTopologyClient) NetworkList(context.Context, client.NetworkListOptions) (client.NetworkListResult, error) {
	return client.NetworkListResult{Items: []network.Summary{{Network: network.Network{ID: "n1", Name: "web_default"}}}}, nil
}

func (f *fakeTopologyClient) ContainerList(_ context.Context, o client.ContainerListOptions) (client.ContainerListResult, error) {
	f.listedAll = o.All
	return client.ContainerListResult{Items: []container.Summary{{
		ID: "c1", Names: []string{"/app"},
		NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{"web_default": {NetworkID: "n1"}}},
	}}}, nil
}

func TestBuildTopologyIncludesStoppedContainers(t *testing.T) {
	cli := &fakeTopologyClient{}
	g, err := buildTopology(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	if !cli.listedAll {
		t.Error("stopped containers were left out")
	}
	if len(g.Links) != 1 || g.Links[0].NetworkID != "n1" || g.Links[0].ContainerID != "c1" {
		t.Errorf("links = %+v", g.Links)
	}
}

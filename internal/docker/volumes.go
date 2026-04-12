package docker

import (
	"context"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
)

func (c *Client) ListVolumes(ctx context.Context) ([]*volume.Volume, error) {
	resp, err := c.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	return resp.Volumes, nil
}

func (c *Client) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	return c.cli.VolumeInspect(ctx, name)
}

func (c *Client) CreateVolume(ctx context.Context, name, driver string, labels map[string]string) (volume.Volume, error) {
	if driver == "" {
		driver = "local"
	}
	return c.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   name,
		Driver: driver,
		Labels: labels,
	})
}

func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	return c.cli.VolumeRemove(ctx, name, force)
}

func (c *Client) PruneVolumes(ctx context.Context) (dtypes.VolumesPruneReport, error) {
	return c.cli.VolumesPrune(ctx, filters.NewArgs())
}

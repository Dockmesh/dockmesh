package docker

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
)

// ErrInvalidVolumeName is returned by CheckVolumeName.
var ErrInvalidVolumeName = errors.New("invalid volume name")

// volumeNamePattern is Docker's own rule for volume names.
var volumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

// CheckVolumeName rejects anything that isn't a Docker volume name. A
// "<source>:/target" bind whose source is a path mounts that host path, so
// every bind built from user, job or archive input must pass this first.
func CheckVolumeName(name string) error {
	if !volumeNamePattern.MatchString(name) {
		return fmt.Errorf("%w %q", ErrInvalidVolumeName, name)
	}
	return nil
}

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

func (c *Client) PruneVolumes(ctx context.Context) (volume.PruneReport, error) {
	return c.cli.VolumesPrune(ctx, filters.NewArgs())
}

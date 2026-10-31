package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

func (c *Client) ListImages(ctx context.Context, all bool) ([]image.Summary, error) {
	return c.cli.ImageList(ctx, image.ListOptions{All: all})
}

func (c *Client) InspectImage(ctx context.Context, id string) (image.InspectResponse, error) {
	info, err := c.cli.ImageInspect(ctx, id)
	return info, err
}

func (c *Client) PullImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, image.PullOptions{})
}

// PullImageWithAuth is PullImage with a pre-built base64-encoded
// X-Registry-Auth blob. registryAuth may be empty — in that case the
// pull is anonymous, matching PullImage's behaviour exactly.
func (c *Client) PullImageWithAuth(ctx context.Context, ref, registryAuth string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: registryAuth})
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) ([]image.DeleteResponse, error) {
	return c.cli.ImageRemove(ctx, id, image.RemoveOptions{Force: force, PruneChildren: true})
}

func (c *Client) PruneImages(ctx context.Context) (image.PruneReport, error) {
	f := filters.NewArgs()
	f.Add("dangling", "true")
	return c.cli.ImagesPrune(ctx, f)
}

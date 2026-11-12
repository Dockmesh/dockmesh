package docker

import (
	"context"
	"io"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func (c *Client) ListImages(ctx context.Context, all bool) ([]image.Summary, error) {
	res, err := c.cli.ImageList(ctx, client.ImageListOptions{All: all})
	return res.Items, err
}

func (c *Client) InspectImage(ctx context.Context, id string) (image.InspectResponse, error) {
	info, err := c.cli.ImageInspect(ctx, id)
	return info.InspectResponse, err
}

func (c *Client) PullImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
}

// PullImageWithAuth is PullImage with a pre-built base64-encoded
// X-Registry-Auth blob. registryAuth may be empty — in that case the
// pull is anonymous, matching PullImage's behaviour exactly.
func (c *Client) PullImageWithAuth(ctx context.Context, ref, registryAuth string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: registryAuth})
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) ([]image.DeleteResponse, error) {
	res, err := c.cli.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	return res.Items, err
}

func (c *Client) PruneImages(ctx context.Context) (image.PruneReport, error) {
	res, err := c.cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: client.Filters{}.Add("dangling", "true")})
	return res.Report, err
}

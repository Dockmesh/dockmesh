package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

func (c *Client) ListImages(ctx context.Context, all bool) ([]image.Summary, error) {
	return c.cli.ImageList(ctx, types.ImageListOptions{All: all})
}

func (c *Client) PullImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	return c.cli.ImagePull(ctx, ref, types.ImagePullOptions{})
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) ([]image.DeleteResponse, error) {
	return c.cli.ImageRemove(ctx, id, types.ImageRemoveOptions{Force: force, PruneChildren: true})
}

func (c *Client) PruneImages(ctx context.Context) (types.ImagesPruneReport, error) {
	f := filters.NewArgs()
	f.Add("dangling", "true")
	return c.cli.ImagesPrune(ctx, f)
}

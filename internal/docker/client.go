package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
)

type Client struct {
	cli *client.Client
}

// New builds a Docker client with API version negotiation so the binary
// keeps working across daemon versions.
func New(ctx context.Context) (*Client, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker ping: %w", err)
	}
	return &Client{cli: cli}, nil
}

func (c *Client) Close() error {
	if c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// Raw exposes the underlying Docker SDK client.
// TODO(phase1): replace with typed wrappers per resource.
func (c *Client) Raw() *client.Client { return c.cli }

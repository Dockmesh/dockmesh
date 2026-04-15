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

// Raw exposes the underlying Docker SDK client. Used by compose, backup,
// updater, proxy and metrics subsystems. Typed per-resource wrappers were
// considered in Phase 1 but deferred — the SDK surface is large and the
// value of wrapping every call is marginal compared to the churn. Keep.
func (c *Client) Raw() *client.Client { return c.cli }

// Wrap adopts an existing low-level docker SDK client. Used by the agent
// where we already have a *client.Client from the heartbeat / request
// handlers and don't want to open a second connection just to satisfy
// the compose.Service constructor.
func Wrap(cli *client.Client) *Client { return &Client{cli: cli} }

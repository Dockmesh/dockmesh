package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// ExecSession bundles the exec ID with its hijacked connection.
type ExecSession struct {
	ID     string
	Hijack types.HijackedResponse
}

// StartExec creates and attaches an interactive exec instance.
// The caller must Close() the returned HijackedResponse.
func (c *Client) StartExec(ctx context.Context, containerID string, cmd []string) (*ExecSession, error) {
	resp, err := c.cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmd,
	})
	if err != nil {
		return nil, err
	}
	hijack, err := c.cli.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{Tty: true})
	if err != nil {
		return nil, err
	}
	return &ExecSession{ID: resp.ID, Hijack: hijack}, nil
}

func (c *Client) ResizeExec(ctx context.Context, execID string, rows, cols uint) error {
	return c.cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
		Height: rows,
		Width:  cols,
	})
}

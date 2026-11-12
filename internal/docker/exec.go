package docker

import (
	"context"

	"github.com/moby/moby/client"
)

// ExecSession bundles the exec ID with its hijacked connection.
type ExecSession struct {
	ID     string
	Hijack client.HijackedResponse
}

// StartExec creates and attaches an interactive exec instance.
// The caller must Close() the returned HijackedResponse.
func (c *Client) StartExec(ctx context.Context, containerID string, cmd []string) (*ExecSession, error) {
	resp, err := c.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          true,
		Cmd:          cmd,
	})
	if err != nil {
		return nil, err
	}
	hijack, err := c.cli.ExecAttach(ctx, resp.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return nil, err
	}
	return &ExecSession{ID: resp.ID, Hijack: hijack.HijackedResponse}, nil
}

func (c *Client) ResizeExec(ctx context.Context, execID string, rows, cols uint) error {
	_, err := c.cli.ExecResize(ctx, execID, client.ExecResizeOptions{
		Height: rows,
		Width:  cols,
	})
	return err
}

package docker

import (
	"bytes"
	"context"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// cappedBuffer keeps the first max bytes and discards the rest, so the
// copy reads the stream to its end and the exec has finished when its
// exit code is read.
type cappedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - int64(b.Len()); room > 0 {
		b.Buffer.Write(p[:min(int64(len(p)), room)])
	}
	return len(p), nil
}

// execExitPoll is how often RunExec re-reads an exec that still shows as
// running after its output stream closed.
var execExitPoll = 50 * time.Millisecond

// RunExec runs cmd in a container without a TTY and returns up to max bytes
// of its stdout and stderr, interleaved as written, plus the exit code.
// Without a TTY Docker multiplexes both streams with frame headers, which
// end up in the output unless they are split off here.
func RunExec(ctx context.Context, cli client.ExecAPIClient, containerID string, cmd []string, max int64) ([]byte, int, error) {
	exec, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, -1, err
	}
	attach, err := cli.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, -1, err
	}
	defer attach.Close()
	// The client doesn't tie the hijacked connection to ctx, so a command
	// that keeps printing would block the copy past any deadline.
	stop := context.AfterFunc(ctx, attach.Close)
	defer stop()
	out := &cappedBuffer{max: max}
	_, _ = stdcopy.StdCopy(out, out, attach.Reader)
	if err := ctx.Err(); err != nil {
		return out.Bytes(), -1, err
	}
	for {
		insp, err := cli.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
		if err != nil {
			return out.Bytes(), -1, err
		}
		if !insp.Running {
			return out.Bytes(), insp.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return out.Bytes(), -1, ctx.Err()
		case <-time.After(execExitPoll):
		}
	}
}

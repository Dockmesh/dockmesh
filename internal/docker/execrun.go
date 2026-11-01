package docker

import (
	"bytes"
	"context"
	"errors"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var errOutputFull = errors.New("exec output limit reached")

// cappedBuffer stops the copy once max bytes are kept.
type cappedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - int64(b.Len())
	if room <= 0 {
		return 0, errOutputFull
	}
	if int64(len(p)) > room {
		b.Buffer.Write(p[:room])
		return int(room), errOutputFull
	}
	return b.Buffer.Write(p)
}

// RunExec runs cmd in a container without a TTY and returns up to max bytes
// of its stdout and stderr, interleaved as written, plus the exit code.
// Without a TTY Docker multiplexes both streams with frame headers, which
// end up in the output unless they are split off here.
func RunExec(ctx context.Context, cli client.ContainerAPIClient, containerID string, cmd []string, max int64) ([]byte, int, error) {
	exec, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, -1, err
	}
	attach, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, -1, err
	}
	defer attach.Close()
	out := &cappedBuffer{max: max}
	_, _ = stdcopy.StdCopy(out, out, attach.Reader)
	insp, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return out.Bytes(), -1, err
	}
	return out.Bytes(), insp.ExitCode, nil
}

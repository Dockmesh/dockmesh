package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dockmesh/dockmesh/internal/docker"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

const volumeTarHelperImage = "busybox:latest"

// tarVolumeHelper spawns a busybox container that tars the named volume
// to stdout. The returned ReadCloser streams the .tar.gz bytes and
// cleans up the helper container on Close. Shared between LocalHost's
// VolumeTar (backup path) and callers that want a raw tar stream.
func tarVolumeHelper(ctx context.Context, dc *docker.Client, volumeName string) (io.ReadCloser, error) {
	cli := dc.Raw()
	// ensure helper image present
	if _, _, err := cli.ImageInspectWithRaw(ctx, volumeTarHelperImage); err != nil {
		rc, pullErr := cli.ImagePull(ctx, volumeTarHelperImage, dtypes.ImagePullOptions{})
		if pullErr != nil {
			return nil, fmt.Errorf("pull %s: %w", volumeTarHelperImage, pullErr)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        volumeTarHelperImage,
		Cmd:          []string{"sh", "-c", "tar czf - -C /source ."},
		AttachStdout: true,
		AttachStderr: true,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "backup-helper",
		},
	}, &container.HostConfig{
		Binds: []string{volumeName + ":/source:ro"},
	}, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create helper: %w", err)
	}
	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("attach: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		hijack.Close()
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start: %w", err)
	}
	return &volumeTarStream{
		ctx:     ctx,
		cli:     cli,
		id:      resp.ID,
		hijack:  hijack.Reader,
		closeFn: hijack.Close,
	}, nil
}

type volumeTarStream struct {
	ctx     context.Context
	cli     dockerClient
	id      string
	hijack  io.Reader
	closeFn func()
	leftover []byte
}

type dockerClient interface {
	ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error
	ContainerWait(ctx context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
}

func (s *volumeTarStream) Read(p []byte) (int, error) {
	// docker multiplexed log frames — strip the 8-byte header to get
	// stdout bytes only. Header: [stream_id, 0, 0, 0, size(4 BE)]
	if len(s.leftover) > 0 {
		n := copy(p, s.leftover)
		s.leftover = s.leftover[n:]
		return n, nil
	}
	var hdr [8]byte
	if _, err := io.ReadFull(s.hijack, hdr[:]); err != nil {
		return 0, err
	}
	size := binary.BigEndian.Uint32(hdr[4:8])
	if size == 0 {
		return 0, nil
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(s.hijack, buf); err != nil {
		return 0, err
	}
	// Only stream 1 = stdout interests us; drop anything else.
	if hdr[0] != 1 {
		return s.Read(p)
	}
	n := copy(p, buf)
	if n < len(buf) {
		s.leftover = append(s.leftover, buf[n:]...)
	}
	return n, nil
}

func (s *volumeTarStream) Close() error {
	s.closeFn()
	// Best-effort wait for exit + remove helper.
	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCh, errCh := s.cli.ContainerWait(waitCtx, s.id, container.WaitConditionNotRunning)
	select {
	case <-waitCh:
	case <-errCh:
	}
	_ = s.cli.ContainerRemove(context.Background(), s.id, container.RemoveOptions{Force: true})
	return nil
}

// execHelper runs a command in an existing container and returns the
// merged stdout/stderr. Used for backup pre-hooks.
func execHelper(ctx context.Context, dc *docker.Client, containerID string, cmd []string) ([]byte, int, error) {
	cli := dc.Raw()
	exec, err := cli.ContainerExecCreate(ctx, containerID, dtypes.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, -1, err
	}
	attach, err := cli.ContainerExecAttach(ctx, exec.ID, dtypes.ExecStartCheck{})
	if err != nil {
		return nil, -1, err
	}
	defer attach.Close()
	var out bytes.Buffer
	// read up to 1 MiB of output — pre-hooks shouldn't print books.
	_, _ = io.CopyN(&out, attach.Reader, 1<<20)
	insp, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return out.Bytes(), -1, err
	}
	return out.Bytes(), insp.ExitCode, nil
}

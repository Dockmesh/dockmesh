package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/dockmesh/dockmesh/internal/docker"
)

const volumeTarHelperImage = "busybox:latest"

// tarVolumeHelper spawns a busybox container that tars the named volume
// to stdout. The returned ReadCloser streams the .tar.gz bytes and
// cleans up the helper container on Close. Shared between LocalHost's
// VolumeTar (backup path) and callers that want a raw tar stream.
func tarVolumeHelper(ctx context.Context, dc *docker.Client, volumeName string) (io.ReadCloser, error) {
	if err := docker.CheckVolumeName(volumeName); err != nil {
		return nil, err
	}
	cli := dc.Raw()
	// ensure helper image present
	if _, err := cli.ImageInspect(ctx, volumeTarHelperImage); err != nil {
		rc, pullErr := cli.ImagePull(ctx, volumeTarHelperImage, image.PullOptions{})
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
		Binds:     []string{volumeName + ":/source:ro"},
		LogConfig: docker.HelperLogConfig,
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
	ctx      context.Context
	cli      dockerClient
	id       string
	hijack   io.Reader
	closeFn  func()
	leftover []byte
	// done is set once tar's output ended; a Close before that stops it.
	done bool
}

type dockerClient interface {
	docker.HelperAPI
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
		if errors.Is(err, io.EOF) {
			s.done = true
			return 0, s.exitErr()
		}
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

// exitErr turns the end of the output into io.EOF only when tar exited 0;
// otherwise the archive is incomplete and the reader must see an error.
func (s *volumeTarStream) exitErr() error {
	waitCtx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	waitCh, errCh := s.cli.ContainerWait(waitCtx, s.id, container.WaitConditionNotRunning)
	select {
	case w := <-waitCh:
		if w.StatusCode != 0 {
			return fmt.Errorf("tar exit %d", w.StatusCode)
		}
		return io.EOF
	case err := <-errCh:
		return fmt.Errorf("wait for tar: %w", err)
	}
}

func (s *volumeTarStream) Close() error {
	s.closeFn()
	_, _ = docker.FinishHelper(s.ctx, s.cli, s.id, !s.done)
	return nil
}

// execHelper runs a command in an existing container and returns the
// merged stdout/stderr. Used for backup pre-hooks.
func execHelper(ctx context.Context, dc *docker.Client, containerID string, cmd []string) ([]byte, int, error) {
	// Up to 1 MiB of output; pre-hooks shouldn't print books.
	return docker.RunExec(ctx, dc.Raw(), containerID, cmd, 1<<20)
}

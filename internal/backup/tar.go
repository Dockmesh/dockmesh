package backup

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dockmesh/dockmesh/internal/docker"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

const helperImage = "alpine:3.19"

// tarVolume runs `tar czf - -C /source .` inside an alpine helper that
// mounts the named volume read-only and streams the gzipped archive on
// stdout. The returned ReadCloser yields the tar bytes; closing it
// removes the helper container.
//
// The helper auto-cleans on exit via AutoRemove=false plus an explicit
// RemoveContainer in the goroutine — AutoRemove racing with Wait() is
// flaky in the SDK so we manage removal ourselves.
func tarVolume(ctx context.Context, dc *docker.Client, volumeName string) (io.ReadCloser, error) {
	if dc == nil {
		return nil, errors.New("docker unavailable")
	}
	cli := dc.Raw()

	if err := ensureHelperImage(ctx, dc); err != nil {
		return nil, err
	}

	cfg := &container.Config{
		Image: helperImage,
		Cmd:   []string{"sh", "-c", "tar czf - -C /source ."},
		AttachStdout: true,
		AttachStderr: true,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "backup-helper",
		},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false,
		Binds:      []string{volumeName + ":/source:ro"},
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create helper: %w", err)
	}

	hijack, err := cli.ContainerAttach(ctx, resp.ID, dtypes.ContainerAttachOptions{
		Stream: true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("attach helper: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		hijack.Close()
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start helper: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		defer hijack.Close()
		// Demux multiplexed stdout/stderr; tar bytes go to pw, stderr is
		// captured for the error message in case the helper fails.
		var stderr stderrBuf
		_, copyErr := stdcopy.StdCopy(pw, &stderr, hijack.Reader)

		statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
		var waitErr error
		var status int64
		select {
		case s := <-statusCh:
			status = s.StatusCode
		case e := <-errCh:
			waitErr = e
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		switch {
		case waitErr != nil:
			pw.CloseWithError(fmt.Errorf("helper wait: %w", waitErr))
		case status != 0:
			pw.CloseWithError(fmt.Errorf("tar exit %d: %s", status, stderr.String()))
		case copyErr != nil:
			pw.CloseWithError(fmt.Errorf("helper copy: %w", copyErr))
		default:
			pw.Close()
		}
	}()

	return pr, nil
}

// untarVolume streams a gzipped tar archive into a named volume by
// running `tar xzf -` inside a helper that mounts the volume read-write.
func untarVolume(ctx context.Context, dc *docker.Client, volumeName string, src io.Reader) error {
	if dc == nil {
		return errors.New("docker unavailable")
	}
	cli := dc.Raw()
	if err := ensureHelperImage(ctx, dc); err != nil {
		return err
	}

	cfg := &container.Config{
		Image: helperImage,
		Cmd:   []string{"sh", "-c", "tar xzf - -C /dest"},
		AttachStdin: true,
		AttachStderr: true,
		OpenStdin: true,
		StdinOnce: true,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "backup-helper",
		},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false,
		Binds:      []string{volumeName + ":/dest"},
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create restore helper: %w", err)
	}
	defer func() { _ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}) }()

	hijack, err := cli.ContainerAttach(ctx, resp.ID, dtypes.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach restore: %w", err)
	}
	defer hijack.Close()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start restore: %w", err)
	}

	// Stream the archive into stdin and close the write side so tar exits.
	go func() {
		_, _ = io.Copy(hijack.Conn, src)
		_ = hijack.CloseWrite()
	}()

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case s := <-statusCh:
		if s.StatusCode != 0 {
			return fmt.Errorf("restore tar exit %d", s.StatusCode)
		}
	case e := <-errCh:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func ensureHelperImage(ctx context.Context, dc *docker.Client) error {
	cli := dc.Raw()
	if _, _, err := cli.ImageInspectWithRaw(ctx, helperImage); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	rc, err := cli.ImagePull(ctx, helperImage, dtypes.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", helperImage, err)
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// stderrBuf is a tiny io.Writer that caps captured output at 4 KB so a
// chatty helper can't blow up our memory.
type stderrBuf struct{ b []byte }

func (s *stderrBuf) Write(p []byte) (int, error) {
	const max = 4 * 1024
	if len(s.b) >= max {
		return len(p), nil
	}
	if len(s.b)+len(p) > max {
		s.b = append(s.b, p[:max-len(s.b)]...)
	} else {
		s.b = append(s.b, p...)
	}
	return len(p), nil
}

func (s *stderrBuf) String() string { return string(s.b) }

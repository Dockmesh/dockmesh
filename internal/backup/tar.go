package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
)

const helperImage = "alpine:3.24"

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
	if err := docker.CheckVolumeName(volumeName); err != nil {
		return nil, err
	}
	cli := dc.Raw()

	if err := ensureHelperImage(ctx, dc); err != nil {
		return nil, err
	}

	cfg := &container.Config{
		Image:        helperImage,
		Cmd:          []string{"sh", "-c", "tar czf - -C /source ."},
		AttachStdout: true,
		AttachStderr: true,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "backup-helper",
		},
	}
	hostCfg := &container.HostConfig{
		Binds:     []string{volumeName + ":/source:ro"},
		LogConfig: docker.HelperLogConfig,
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create helper: %w", err)
	}

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
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
		// Demux multiplexed stdout/stderr; tar bytes go to pw, stderr is
		// captured for the error message in case the helper fails.
		var stderr stderrBuf
		_, copyErr := stdcopy.StdCopy(pw, &stderr, hijack.Reader)
		hijack.Close()
		// A failed copy means the reader went away; tar must not keep
		// running into a stream nobody reads.
		status, waitErr := docker.FinishHelper(ctx, cli, resp.ID, copyErr != nil)

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
// running agents.VolumeImportScript inside a helper that mounts the volume
// read-write. replace swaps the volume's contents for the archive's once
// tar succeeded; needBytes is the free space that needs.
func untarVolume(ctx context.Context, dc *docker.Client, volumeName string, src io.Reader, replace bool, needBytes int64) (err error) {
	if dc == nil {
		return errors.New("docker unavailable")
	}
	if err := docker.CheckVolumeName(volumeName); err != nil {
		return err
	}
	cli := dc.Raw()
	if err := ensureHelperImage(ctx, dc); err != nil {
		return err
	}
	// Registered first so it runs after the import helper is gone.
	defer func() {
		if err != nil && replace {
			recoverVolume(ctx, dc, volumeName)
		}
	}()

	cfg := &container.Config{
		Image:        helperImage,
		Cmd:          []string{"sh", "-c", agents.VolumeImportScript(replace, needBytes)},
		AttachStdin:  true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    true,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "backup-helper",
		},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false,
		Binds:      []string{volumeName + ":/dest"},
		LogConfig:  docker.HelperLogConfig,
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create restore helper: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.HelperTimeout)
		defer cancel()
		_ = cli.ContainerRemove(rctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
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
	// A source that breaks off kills the helper instead, so tar never takes
	// the cut-off stream for the whole archive.
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(hijack.Conn, src)
		if err != nil {
			kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.HelperTimeout)
			_ = cli.ContainerKill(kctx, resp.ID, "KILL")
			cancel()
		} else {
			_ = hijack.CloseWrite()
		}
		copyDone <- err
	}()

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	var exitCode int64
	select {
	case s := <-statusCh:
		exitCode = s.StatusCode
	case e := <-errCh:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
	// A failing source is the real cause behind a tar exit code, and tar
	// does not notice every stream that breaks off, so check it first.
	_ = hijack.Conn.Close()
	if err := <-copyDone; err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("restore stream: %w", err)
	}
	return agents.VolumeImportExitError(exitCode, replace)
}

// recoverVolume runs agents.VolumeRecoverScript after a failed replace
// restore, so the volume keeps its previous contents and no staging
// directory is left behind.
func recoverVolume(ctx context.Context, dc *docker.Client, volumeName string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.HelperTimeout)
	defer cancel()
	cli := dc.Raw()
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:  helperImage,
		Cmd:    []string{"sh", "-c", agents.VolumeRecoverScript()},
		Labels: map[string]string{"dockmesh.managed": "true", "dockmesh.component": "backup-helper"},
	}, &container.HostConfig{
		Binds:     []string{volumeName + ":/dest"},
		LogConfig: docker.HelperLogConfig,
	}, nil, nil, "")
	if err != nil {
		slog.Warn("restore: clean up volume after a failed restore", "volume", volumeName, "err", err)
		return
	}
	defer func() { _ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}) }()
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		slog.Warn("restore: clean up volume after a failed restore", "volume", volumeName, "err", err)
		return
	}
	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case s := <-statusCh:
		if s.StatusCode != 0 {
			slog.Warn("restore: clean up volume after a failed restore", "volume", volumeName, "exit", s.StatusCode)
		}
	case err := <-errCh:
		slog.Warn("restore: clean up volume after a failed restore", "volume", volumeName, "err", err)
	}
}

func ensureHelperImage(ctx context.Context, dc *docker.Client) error {
	cli := dc.Raw()
	if _, err := cli.ImageInspect(ctx, helperImage); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}
	rc, err := cli.ImagePull(ctx, helperImage, image.PullOptions{})
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

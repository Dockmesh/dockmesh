package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// OpenVolumeFileOnHost opens a regular file inside the volume for a full
// download, directly when this process may read the mountpoint and
// through a helper container when not. The caller closes the reader.
func OpenVolumeFileOnHost(ctx context.Context, cli *client.Client, volumeName, mountpoint, sub string) (io.ReadCloser, error) {
	if _, err := SanitizeVolumePath(mountpoint, sub); err != nil {
		return nil, err
	}
	if directAccessible(mountpoint) {
		rc, err := openVolumeFile(mountpoint, sub)
		if !errors.Is(err, fs.ErrPermission) {
			return rc, err
		}
	}
	return openFileViaHelper(ctx, cli, volumeName, sub)
}

type rootedFile struct {
	*os.File
	root *os.Root
}

func (f *rootedFile) Close() error {
	err := f.File.Close()
	_ = f.root.Close()
	return err
}

func openVolumeFile(mountpoint, sub string) (io.ReadCloser, error) {
	root, _, f, err := openInVolume(mountpoint, sub)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrVolumeNotFile
	}
	if err != nil {
		f.Close()
		root.Close()
		return nil, err
	}
	return &rootedFile{File: f, root: root}, nil
}

func downloadFileScript(target string) string {
	return helperResolveScript(target) + fmt.Sprintf(`[ -f "$r" ] || exit %d
exec cat "$r"
`, helperExitNotFile)
}

// openFileViaHelper streams the file out of a helper container over an
// attach, so the content never passes through the container's log
// driver. A failed check or read ends the stream with its error.
func openFileViaHelper(ctx context.Context, cli *client.Client, volumeName, sub string) (io.ReadCloser, error) {
	target, err := helperTarget(sub)
	if err != nil {
		return nil, err
	}
	if err := ensureBrowseHelperImage(ctx, cli); err != nil {
		return nil, err
	}
	cfg, hostCfg := helperContainerConfig(volumeName, downloadFileScript(target))
	cfg.AttachStdout, cfg.AttachStderr = true, true
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg})
	if err != nil {
		return nil, fmt.Errorf("volume helper: create: %w", err)
	}
	remove := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = cli.ContainerRemove(rctx, created.ID, client.ContainerRemoveOptions{Force: true})
	}
	attach, err := cli.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		remove()
		return nil, fmt.Errorf("volume helper: attach: %w", err)
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		attach.Close()
		remove()
		return nil, fmt.Errorf("volume helper: start: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		defer remove()
		stderr := &cappedBuffer{max: 4096}
		_, copyErr := stdcopy.StdCopy(pw, stderr, attach.Reader)
		attach.Close()
		if copyErr != nil {
			pw.CloseWithError(copyErr)
			return
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		wait := cli.ContainerWait(wctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case err := <-wait.Error:
			pw.CloseWithError(fmt.Errorf("volume helper: wait: %w", err))
		case st := <-wait.Result:
			pw.CloseWithError(helperExitError(helperResult{exitCode: st.StatusCode, stderr: stderr.String()}, target))
		}
	}()
	return pr, nil
}

func ensureBrowseHelperImage(ctx context.Context, cli *client.Client) error {
	if _, err := cli.ImageInspect(ctx, helperImage); err == nil {
		return nil
	}
	if err := docker.PullAnonymous(ctx, cli, helperImage); err != nil {
		return fmt.Errorf("volume helper: pull %s: %w", helperImage, err)
	}
	return nil
}

type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }

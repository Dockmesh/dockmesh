package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Docker volumes live at /var/lib/docker/volumes/<name>/_data which is
// root:root 0700 on most hosts, while the server and the agent run as
// unprivileged users. When direct access is denied, a short-lived alpine
// container mounts the volume read-only and lists or reads it as root
// inside its own namespace.

const (
	// Pinned to the multi-arch index digest so a retagged or replaced
	// image never runs with access to the volume.
	helperImage = "alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f"
	helperMount = "/mnt/target"

	helperMaxEntries   = 10000
	helperListingLimit = 16 << 20
	helperPidsLimit    = 64
	helperMemoryLimit  = 64 << 20
)

// Exit codes the helper scripts use to report containment and type
// failures, so they map onto the same sentinels as direct access.
const (
	helperExitEscape   = 70
	helperExitNotFound = 71
	helperExitNotDir   = 72
	helperExitNotFile  = 73
)

// BrowseVolumeOnHost lists sub inside the volume, reading the mountpoint
// directly when this process may and through a helper container when not.
func BrowseVolumeOnHost(ctx context.Context, cli *client.Client, volumeName, mountpoint, sub string) ([]VolumeEntry, error) {
	if _, err := SanitizeVolumePath(mountpoint, sub); err != nil {
		return nil, err
	}
	if directAccessible(mountpoint) {
		entries, err := BrowseVolume(mountpoint, sub)
		if !errors.Is(err, fs.ErrPermission) {
			return entries, err
		}
	}
	return browseDirViaHelper(ctx, dockerHelperRunner(cli, volumeName), sub)
}

// ReadVolumeFileOnHost is the file-read counterpart of BrowseVolumeOnHost.
func ReadVolumeFileOnHost(ctx context.Context, cli *client.Client, volumeName, mountpoint, sub string, maxBytes int64) (*VolumeFileResult, error) {
	if _, err := SanitizeVolumePath(mountpoint, sub); err != nil {
		return nil, err
	}
	if directAccessible(mountpoint) {
		res, err := ReadVolumeFile(mountpoint, sub, maxBytes)
		if !errors.Is(err, fs.ErrPermission) {
			return res, err
		}
	}
	return readFileViaHelper(ctx, dockerHelperRunner(cli, volumeName), sub, maxBytes)
}

func directAccessible(mountpoint string) bool {
	_, err := os.Stat(mountpoint)
	return err == nil
}

type helperResult struct {
	stdout   string
	stderr   string
	exitCode int64
}

// helperRunner runs script and reads at most limit bytes of its output.
type helperRunner func(ctx context.Context, script string, limit int64) (helperResult, error)

// helperTarget maps sub onto the helper's mount, rejecting lexical
// escapes before any container is started.
func helperTarget(sub string) (string, error) {
	if len(sub) > MaxBrowsePathLen {
		return "", ErrVolumePathTooLong
	}
	full := path.Join(helperMount, strings.TrimPrefix(sub, "/"))
	if full != helperMount && !strings.HasPrefix(full, helperMount+"/") {
		return "", ErrVolumePathEscape
	}
	return full, nil
}

// helperResolveScript resolves the target with realpath inside the
// helper and refuses anything that lands outside the volume mount, so a
// symlink in the volume answers the same escape error as direct access.
func helperResolveScript(target string) string {
	return fmt.Sprintf(`t=%s
[ -e "$t" ] || exit %d
r=$(realpath "$t") || exit %d
case "$r" in %s|%s/*) ;; *) exit %d ;; esac
`, shellEscape(target), helperExitNotFound, helperExitNotFound, helperMount, helperMount, helperExitEscape)
}

func browseDirScript(target string) string {
	// BusyBox find has no -printf, so stat runs per entry. Globbing ./*
	// keeps names starting with '-' from being read as options, and names
	// and link targets are base64 so a newline cannot forge a row.
	// Output per line: <type>|<size>|<octal-mode>|<mtime>|<b64 linkdest>|<b64 name>
	return helperResolveScript(target) + fmt.Sprintf(`[ -d "$r" ] || exit %d
cd "$r" || exit %d
i=0
for p in ./* ./.[!.]* ./..?*; do
  [ -e "$p" ] || [ -L "$p" ] || continue
  i=$((i+1))
  [ "$i" -le %d ] || break
  n=$(printf '%%s' "${p#./}" | base64 | tr -d '\n')
  m=$(stat -c '%%s|%%a|%%Y' "$p") || continue
  if [ -L "$p" ]; then
    l=$(readlink "$p" | tr -d '\n' | base64 | tr -d '\n')
    printf 'l|%%s|%%s|%%s\n' "$m" "$l" "$n"
  elif [ -d "$p" ]; then
    printf 'd|%%s||%%s\n' "$m" "$n"
  else
    printf 'f|%%s||%%s\n' "$m" "$n"
  fi
done
`, helperExitNotDir, helperExitNotFound, helperMaxEntries)
}

func readFileScript(target string, maxBytes int64) string {
	// head -c stops after maxBytes so a huge file is never streamed whole.
	return helperResolveScript(target) + fmt.Sprintf(`[ -f "$r" ] || exit %d
stat -c '%%s' "$r" && head -c %d "$r"
`, helperExitNotFile, maxBytes)
}

func helperExitError(res helperResult, target string) error {
	switch res.exitCode {
	case 0:
		return nil
	case helperExitEscape:
		return ErrVolumePathEscape
	case helperExitNotFound:
		return fmt.Errorf("%s: %w", strings.TrimPrefix(target, helperMount), fs.ErrNotExist)
	case helperExitNotDir:
		return ErrVolumeNotDir
	case helperExitNotFile:
		return ErrVolumeNotFile
	}
	return fmt.Errorf("volume helper: exit %d: %s", res.exitCode, strings.TrimSpace(res.stderr))
}

func browseDirViaHelper(ctx context.Context, run helperRunner, sub string) ([]VolumeEntry, error) {
	target, err := helperTarget(sub)
	if err != nil {
		return nil, err
	}
	res, err := run(ctx, browseDirScript(target), helperListingLimit)
	if err != nil {
		return nil, err
	}
	if err := helperExitError(res, target); err != nil {
		return nil, err
	}
	return parseHelperListing(res.stdout), nil
}

// parseHelperListing skips rows it cannot parse and a last row that the
// output limit cut off.
func parseHelperListing(raw string) []VolumeEntry {
	out := make([]VolumeEntry, 0)
	lines := strings.Split(raw, "\n")
	for _, line := range lines[:len(lines)-1] {
		if len(out) >= helperMaxEntries {
			break
		}
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 6 {
			continue
		}
		name, err := base64.StdEncoding.DecodeString(parts[5])
		if err != nil || len(name) == 0 {
			continue
		}
		linkDest, err := base64.StdEncoding.DecodeString(parts[4])
		if err != nil {
			continue
		}
		sizeBytes, _ := strconv.ParseInt(parts[1], 10, 64)
		modeOctal, _ := strconv.ParseUint(parts[2], 8, 32)
		mtimeSecs, _ := strconv.ParseInt(parts[3], 10, 64)

		entry := VolumeEntry{
			Name:    string(name),
			Size:    sizeBytes,
			Mode:    fmt.Sprintf("%04o", modeOctal),
			ModTime: time.Unix(mtimeSecs, 0),
		}
		switch parts[0] {
		case "d":
			entry.Type = "dir"
		case "l":
			entry.Type = "symlink"
			entry.LinkDest = string(linkDest)
		default:
			entry.Type = "file"
		}
		out = append(out, entry)
	}
	return out
}

func readFileViaHelper(ctx context.Context, run helperRunner, sub string, maxBytes int64) (*VolumeFileResult, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	target, err := helperTarget(sub)
	if err != nil {
		return nil, err
	}
	res, err := run(ctx, readFileScript(target, maxBytes), maxBytes+4096)
	if err != nil {
		return nil, err
	}
	if err := helperExitError(res, target); err != nil {
		return nil, err
	}

	// First line is the full file size, the rest up to maxBytes of content.
	nl := strings.IndexByte(res.stdout, '\n')
	if nl < 0 {
		return &VolumeFileResult{}, nil
	}
	totalSize, _ := strconv.ParseInt(strings.TrimSpace(res.stdout[:nl]), 10, 64)
	body := []byte(res.stdout[nl+1:])
	if int64(len(body)) > maxBytes {
		body = body[:maxBytes]
	}
	return &VolumeFileResult{
		Size:      totalSize,
		Content:   body,
		Truncated: totalSize > int64(len(body)),
		Binary:    looksBinary(body),
	}, nil
}

// shellEscape wraps a path in single quotes and escapes internal
// single quotes the POSIX way.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func helperContainerConfig(volumeName, script string) (*container.Config, *container.HostConfig) {
	pids := int64(helperPidsLimit)
	cfg := &container.Config{
		Image:      helperImage,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{script},
		Labels: map[string]string{
			"com.dockmesh.helper": "volume-browse",
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode:    "none",
		ReadonlyRootfs: true,
		// DAC_OVERRIDE lets root read files of any owner in the volume; the
		// mount is read-only, so it grants no writes.
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"DAC_OVERRIDE"},
		SecurityOpt: []string{"no-new-privileges"},
		Resources: container.Resources{
			PidsLimit: &pids,
			Memory:    helperMemoryLimit,
		},
		Mounts: []mount.Mount{{
			Type:     mount.TypeVolume,
			Source:   volumeName,
			Target:   helperMount,
			ReadOnly: true,
		}},
	}
	return cfg, hostCfg
}

// dockerHelperRunner runs a script in a throwaway helper container with
// the volume mounted, collects its output and removes the container.
func dockerHelperRunner(cli *client.Client, volumeName string) helperRunner {
	return func(ctx context.Context, script string, limit int64) (helperResult, error) {
		if _, _, err := cli.ImageInspectWithRaw(ctx, helperImage); err != nil {
			rc, pullErr := cli.ImagePull(ctx, helperImage, dtypes.ImagePullOptions{})
			if pullErr != nil {
				return helperResult{}, fmt.Errorf("volume helper: pull %s: %w", helperImage, pullErr)
			}
			_, _ = io.Copy(io.Discard, rc)
			rc.Close()
		}

		cfg, hostCfg := helperContainerConfig(volumeName, script)
		createResp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
		if err != nil {
			return helperResult{}, fmt.Errorf("volume helper: create: %w", err)
		}
		defer func() {
			_ = cli.ContainerRemove(context.Background(), createResp.ID, container.RemoveOptions{Force: true})
		}()

		if err := cli.ContainerStart(ctx, createResp.ID, container.StartOptions{}); err != nil {
			return helperResult{}, fmt.Errorf("volume helper: start: %w", err)
		}

		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var exitCode int64
		statusCh, errCh := cli.ContainerWait(waitCtx, createResp.ID, container.WaitConditionNotRunning)
		select {
		case err := <-errCh:
			if err != nil {
				return helperResult{}, fmt.Errorf("volume helper: wait: %w", err)
			}
		case st := <-statusCh:
			if st.Error != nil {
				return helperResult{}, fmt.Errorf("volume helper: wait: %s", st.Error.Message)
			}
			exitCode = st.StatusCode
		}

		logs, err := cli.ContainerLogs(ctx, createResp.ID, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		})
		if err != nil {
			return helperResult{}, fmt.Errorf("volume helper: logs: %w", err)
		}
		defer logs.Close()

		var stdout, stderr bytes.Buffer
		if _, err := stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(logs, limit)); err != nil {
			return helperResult{}, fmt.Errorf("volume helper: stream: %w", err)
		}
		return helperResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}, nil
	}
}

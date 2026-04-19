package host

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

// Docker volumes live at /var/lib/docker/volumes/<name>/_data which is
// root:root 0700 on most hosts. The Dockmesh process runs as an
// unprivileged user, so direct filesystem access returns EACCES.
//
// These helpers spawn a short-lived alpine container that mounts the
// target volume, runs a `find` to list or `cat` to read, and streams
// output back via stdout. The container runs as root inside its own
// namespace, so it sees volume contents without touching host perms.
//
// alpine:latest is ~5MB; most Dockmesh hosts have it already. If not,
// Docker pulls it on the first browse and caches for subsequent runs.

const helperImage = "alpine:latest"

// dockerClientFor is a tiny accessor so the helper can use the
// package-level Raw() without pulling in the compose package's
// conventions. LocalHost already holds a *docker.Client.
func dockerClientFor(cli *docker.Client) *docker.Client { return cli }

// BrowseDirViaHelper lists one directory inside a Docker volume by
// running `find` inside a throwaway alpine container.
func BrowseDirViaHelper(ctx context.Context, cli *docker.Client, volumeName, sub string) ([]VolumeEntry, error) {
	// Ensure the sub path is relative + sane. The ExtractMountpoint /
	// SanitizeVolumePath helpers are filesystem-oriented; here we only
	// need a path-relative check.
	sub = strings.TrimPrefix(strings.TrimPrefix(sub, "/"), "./")
	if strings.Contains(sub, "..") {
		return nil, fmt.Errorf("invalid subpath")
	}

	// Output format per line: <type>|<size>|<octal-mode>|<mtime>|<linkdest>|<name>
	// BusyBox alpine's find doesn't have GNU's -printf, so we loop
	// through `ls -A` and run `stat` per entry. Filenames with literal
	// '|' or newlines will mis-parse (rare for container volumes); the
	// browse UI skips unparseable rows rather than crashing.
	mountTarget := "/mnt/target"
	if sub != "" {
		mountTarget = "/mnt/target/" + sub
	}
	script := `cd ` + shellEscape(mountTarget) + ` && ls -A | while IFS= read -r n; do
  if [ -L "$n" ]; then
    l=$(readlink "$n")
    printf 'l|0|%s|%s|%s|%s\n' "$(stat -c '%a' "$n")" "$(stat -c '%Y' "$n")" "$l" "$n"
  elif [ -d "$n" ]; then
    printf 'd|%s|%s|%s||%s\n' "$(stat -c '%s' "$n")" "$(stat -c '%a' "$n")" "$(stat -c '%Y' "$n")" "$n"
  else
    printf 'f|%s|%s|%s||%s\n' "$(stat -c '%s' "$n")" "$(stat -c '%a' "$n")" "$(stat -c '%Y' "$n")" "$n"
  fi
done`

	raw, err := runHelperCommand(ctx, cli, volumeName, script)
	if err != nil {
		return nil, err
	}

	out := make([]VolumeEntry, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 6)
		if len(parts) != 6 {
			continue
		}
		sizeBytes, _ := strconv.ParseInt(parts[1], 10, 64)
		modeOctal, _ := strconv.ParseUint(parts[2], 8, 32)
		mtimeSecs, _ := strconv.ParseInt(parts[3], 10, 64)

		entry := VolumeEntry{
			Name:    parts[5],
			Size:    sizeBytes,
			Mode:    fmt.Sprintf("%04o", modeOctal),
			ModTime: time.Unix(mtimeSecs, 0),
		}
		switch parts[0] {
		case "d":
			entry.Type = "dir"
		case "l":
			entry.Type = "symlink"
			entry.LinkDest = parts[4]
		default:
			entry.Type = "file"
		}
		out = append(out, entry)
	}
	return out, nil
}

// ReadFileViaHelper returns the content of a file inside a Docker
// volume, capped at maxBytes.
func ReadFileViaHelper(ctx context.Context, cli *docker.Client, volumeName, sub string, maxBytes int64) (*VolumeFileResult, error) {
	sub = strings.TrimPrefix(strings.TrimPrefix(sub, "/"), "./")
	if strings.Contains(sub, "..") || sub == "" {
		return nil, fmt.Errorf("invalid subpath")
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	// head -c is safer than cat | head here — it reads at most N bytes
	// even from a pipe, so we don't stream a 5GiB log file into
	// container stdout just to throw most of it away.
	script := fmt.Sprintf(
		`stat -c '%%s' /mnt/target/%s && head -c %d /mnt/target/%s`,
		shellEscape(sub), maxBytes, shellEscape(sub),
	)

	raw, err := runHelperCommand(ctx, cli, volumeName, script)
	if err != nil {
		return nil, err
	}

	// First line is the full file size, rest is up to maxBytes of content.
	nl := strings.IndexByte(raw, '\n')
	if nl < 0 {
		return &VolumeFileResult{}, nil
	}
	totalSize, _ := strconv.ParseInt(strings.TrimSpace(raw[:nl]), 10, 64)
	body := []byte(raw[nl+1:])
	if int64(len(body)) > maxBytes {
		body = body[:maxBytes]
	}
	return &VolumeFileResult{
		Size:      totalSize,
		Content:   body,
		Truncated: totalSize > int64(len(body)),
	}, nil
}

// shellEscape wraps a path in single quotes and escapes internal
// single quotes the POSIX way. Covers names with spaces / specials
// without shelling out through a shell's argv parsing quirks.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runHelperCommand is the shared spawn/attach/remove plumbing. Runs
// `sh -c <script>` inside alpine:latest with the target volume mounted
// at /mnt/target, returns combined stdout, and removes the container.
// Errors include anything the alpine container wrote to stderr, so
// permission or ENOENT inside the volume surface cleanly.
func runHelperCommand(ctx context.Context, cli *docker.Client, volumeName, script string) (string, error) {
	raw := dockerClientFor(cli).Raw()

	// Pull alpine on demand. Best-effort — if it's already present
	// docker returns immediately. If the pull fails (offline host),
	// we surface a clear error.
	if _, _, err := raw.ImageInspectWithRaw(ctx, helperImage); err != nil {
		rc, pullErr := raw.ImagePull(ctx, helperImage, dtypes.ImagePullOptions{})
		if pullErr != nil {
			return "", fmt.Errorf("volume helper: pull %s: %w", helperImage, pullErr)
		}
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}

	cfg := &container.Config{
		Image: helperImage,
		Cmd:   []string{"sh", "-c", script},
		Labels: map[string]string{
			"com.dockmesh.helper": "volume-browse",
		},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false, // we remove manually so we can grab logs
		ReadonlyRootfs: true,
		Mounts: []mount.Mount{{
			Type:     mount.TypeVolume,
			Source:   volumeName,
			Target:   "/mnt/target",
			ReadOnly: true,
		}},
	}
	createResp, err := raw.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("volume helper: create: %w", err)
	}
	defer raw.ContainerRemove(context.Background(), createResp.ID, container.RemoveOptions{Force: true})

	if err := raw.ContainerStart(ctx, createResp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("volume helper: start: %w", err)
	}

	// Wait for the container to exit. Cap at 30s so a pathological
	// script can't hang the browse forever.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	statusCh, errCh := raw.ContainerWait(waitCtx, createResp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return "", fmt.Errorf("volume helper: wait: %w", err)
		}
	case <-statusCh:
	}

	logs, err := raw.ContainerLogs(ctx, createResp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("volume helper: logs: %w", err)
	}
	defer logs.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		return "", fmt.Errorf("volume helper: stream: %w", err)
	}
	if stderr.Len() > 0 && stdout.Len() == 0 {
		return "", fmt.Errorf("volume helper: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

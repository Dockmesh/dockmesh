package host

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
)

// LocalHost wraps the embedded docker.Client. It's identified by the
// fixed id "local".
type LocalHost struct {
	cli *docker.Client
}

func NewLocal(cli *docker.Client) *LocalHost { return &LocalHost{cli: cli} }

func (h *LocalHost) ID() string   { return "local" }
func (h *LocalHost) Name() string { return "Local" }

func (h *LocalHost) ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.ListContainers(ctx, all)
}

func (h *LocalHost) InspectContainer(ctx context.Context, id string) (dtypes.ContainerJSON, error) {
	if h.cli == nil || !h.cli.Connected() {
		return dtypes.ContainerJSON{}, ErrNoDocker
	}
	return h.cli.InspectContainer(ctx, id)
}

func (h *LocalHost) StartContainer(ctx context.Context, id string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.StartContainer(ctx, id)
}

func (h *LocalHost) StopContainer(ctx context.Context, id string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.StopContainer(ctx, id)
}

func (h *LocalHost) RestartContainer(ctx context.Context, id string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.RestartContainer(ctx, id)
}

func (h *LocalHost) RemoveContainer(ctx context.Context, id string, force bool) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.RemoveContainer(ctx, id, force)
}

func (h *LocalHost) PauseContainer(ctx context.Context, id string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.PauseContainer(ctx, id)
}

func (h *LocalHost) UnpauseContainer(ctx context.Context, id string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.UnpauseContainer(ctx, id)
}

func (h *LocalHost) KillContainer(ctx context.Context, id, signal string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return h.cli.KillContainer(ctx, id, signal)
}

func (h *LocalHost) ContainerLogs(ctx context.Context, id, tail string, follow bool) (io.ReadCloser, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.ContainerLogs(ctx, id, tail, follow)
}

func (h *LocalHost) ContainerStats(ctx context.Context, id string) (io.ReadCloser, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.ContainerStats(ctx, id)
}

func (h *LocalHost) StartExec(ctx context.Context, id string, cmd []string) (ExecSession, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	sess, err := h.cli.StartExec(ctx, id, cmd)
	if err != nil {
		return nil, err
	}
	return &localExecSession{cli: h.cli, sess: sess, ctx: ctx}, nil
}

// DeployStack writes compose+env to a temp dir, parses, and runs the
// shared compose executor against the local docker daemon. Same code
// path as the agent's deploy handler — proves the seam works.
func (h *LocalHost) DeployStack(ctx context.Context, name, composeYAML, envContent string) (*compose.DeployResult, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	dir, cleanup, err := writeStagingDir(name, composeYAML, envContent)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := compose.LoadProject(ctx, dir, name, envContent)
	if err != nil {
		return nil, err
	}
	svc := compose.NewService(h.cli, nil)
	return svc.DeployProject(ctx, proj)
}

func (h *LocalHost) StopStack(ctx context.Context, name string) error {
	if h.cli == nil || !h.cli.Connected() {
		return ErrNoDocker
	}
	return compose.NewService(h.cli, nil).Stop(ctx, name)
}

func (h *LocalHost) StackStatus(ctx context.Context, name string) ([]compose.StatusEntry, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return compose.NewService(h.cli, nil).Status(ctx, name)
}

// writeStagingDir creates a tmp directory containing compose.yaml and an
// optional .env file. Used both by LocalHost.DeployStack on the central
// server (so we go through the same parse path as the agent) and by the
// agent's own deploy handler.
func writeStagingDir(name, composeYAML, envContent string) (string, func(), error) {
	base, err := os.MkdirTemp("", "dockmesh-deploy-"+name+"-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(base) }
	if err := os.WriteFile(filepath.Join(base, "compose.yaml"), []byte(composeYAML), 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if envContent != "" {
		if err := os.WriteFile(filepath.Join(base, ".env"), []byte(envContent), 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return base, cleanup, nil
}

// WriteStagingDir is the exported helper the agent uses to materialise
// the same compose+env layout into its own /var/lib/dockmesh/staging.
func WriteStagingDir(name, composeYAML, envContent string) (string, func(), error) {
	return writeStagingDir(name, composeYAML, envContent)
}

// localExecSession wraps the docker hijacked response in the ExecSession
// interface. The Conn carries stdin (Write); the Reader carries the
// merged tty stdout.
type localExecSession struct {
	cli  *docker.Client
	sess *docker.ExecSession
	ctx  context.Context
}

func (s *localExecSession) Read(p []byte) (int, error)  { return s.sess.Hijack.Reader.Read(p) }
func (s *localExecSession) Write(p []byte) (int, error) { return s.sess.Hijack.Conn.Write(p) }
func (s *localExecSession) Resize(rows, cols uint) error {
	return s.cli.ResizeExec(s.ctx, s.sess.ID, rows, cols)
}
func (s *localExecSession) Close() error {
	s.sess.Hijack.Close()
	return nil
}

func (h *LocalHost) ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.ListImages(ctx, all)
}

func (h *LocalHost) RemoveImage(ctx context.Context, id string, force bool) ([]dtypes.ImageDeleteResponseItem, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.RemoveImage(ctx, id, force)
}

func (h *LocalHost) PruneImages(ctx context.Context) (dtypes.ImagesPruneReport, error) {
	if h.cli == nil || !h.cli.Connected() {
		return dtypes.ImagesPruneReport{}, ErrNoDocker
	}
	return h.cli.PruneImages(ctx)
}

func (h *LocalHost) ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return h.cli.ListNetworks(ctx)
}

func (h *LocalHost) InspectNetwork(ctx context.Context, id string) (dtypes.NetworkResource, error) {
	if h.cli == nil || !h.cli.Connected() {
		return dtypes.NetworkResource{}, ErrNoDocker
	}
	return h.cli.InspectNetwork(ctx, id)
}

func (h *LocalHost) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	if h.cli == nil || !h.cli.Connected() {
		return volume.Volume{}, ErrNoDocker
	}
	return h.cli.InspectVolume(ctx, name)
}

// VolumeBrowseEntries resolves the requested path against the volume's
// mountpoint on the docker host's own filesystem, then walks it via
// the shared helper. P.11.8 — admin-only at the handler layer.
// VolumeBrowseEntries first tries direct filesystem access via the
// volume's mountpoint. On EACCES (common — Docker volumes are
// root:root 0700 and Dockmesh runs as an unprivileged user) we fall
// back to spawning a short-lived alpine container with the volume
// mounted, which sees everything as root inside its own namespace.
func (h *LocalHost) VolumeBrowseEntries(ctx context.Context, name, subpath string) ([]VolumeEntry, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	vol, err := h.cli.InspectVolume(ctx, name)
	if err != nil {
		return nil, err
	}
	mp, err := ExtractMountpoint(vol.Mountpoint)
	if err != nil {
		return nil, err
	}
	abs, err := SanitizeVolumePath(mp, subpath)
	if err != nil {
		return nil, err
	}
	entries, err := BrowseDir(abs)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		return BrowseDirViaHelper(ctx, h.cli, name, subpath)
	}
	return entries, err
}

func (h *LocalHost) VolumeReadFile(ctx context.Context, name, subpath string, maxBytes int64) (*VolumeFileResult, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	vol, err := h.cli.InspectVolume(ctx, name)
	if err != nil {
		return nil, err
	}
	mp, err := ExtractMountpoint(vol.Mountpoint)
	if err != nil {
		return nil, err
	}
	abs, err := SanitizeVolumePath(mp, subpath)
	if err != nil {
		return nil, err
	}
	res, err := ReadFile(abs, maxBytes)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		return ReadFileViaHelper(ctx, h.cli, name, subpath, maxBytes)
	}
	return res, err
}

// VolumeTar spawns a busybox helper against the docker socket and
// returns a tar.gz stream of the volume. FINDING-33 multi-host backup.
func (h *LocalHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	return tarVolumeHelper(ctx, h.cli, name)
}

// ContainerExec runs cmd inside the container, collects stdout+stderr.
// Used by backup pre-hooks.
func (h *LocalHost) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, -1, ErrNoDocker
	}
	return execHelper(ctx, h.cli, containerID, cmd)
}

func (h *LocalHost) ListVolumes(ctx context.Context) ([]any, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	vols, err := h.cli.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(vols))
	for _, v := range vols {
		out = append(out, v)
	}
	return out, nil
}

func (h *LocalHost) ScaleService(ctx context.Context, name, composeYAML, envContent, service string, replicas int) (*compose.ScaleResult, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	dir, cleanup, err := writeStagingDir(name, composeYAML, envContent)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := compose.LoadProject(ctx, dir, name, envContent)
	if err != nil {
		return nil, err
	}
	return compose.NewService(h.cli, nil).ScaleService(ctx, proj, service, replicas)
}

func (h *LocalHost) CheckScale(ctx context.Context, name, composeYAML, envContent, service string) (*compose.ScaleCheck, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	dir, cleanup, err := writeStagingDir(name, composeYAML, envContent)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := compose.LoadProject(ctx, dir, name, envContent)
	if err != nil {
		return nil, err
	}
	return compose.NewService(h.cli, nil).CheckScale(ctx, proj, service)
}

// RollingReplace runs a rolling replacement of a service's replicas
// against this local host. Remote hosts don't implement this yet —
// the agent protocol gains a matching frame type in a follow-up slice.
// P.12.5b.
func (h *LocalHost) RollingReplace(ctx context.Context, name, composeYAML, envContent, service string, opts compose.RollingOptions) (*compose.RollingResult, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	dir, cleanup, err := writeStagingDir(name, composeYAML, envContent)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := compose.LoadProject(ctx, dir, name, envContent)
	if err != nil {
		return nil, err
	}
	return compose.NewService(h.cli, nil).RollingReplace(ctx, proj, service, opts)
}

// SystemMetrics reads host-level CPU / memory / disk / uptime via the
// system package. On Linux it reads /proc and statfs; on other platforms
// (dev builds) the system package stub returns zero values so the
// dashboard still renders without crashing.
func (h *LocalHost) SystemMetrics(ctx context.Context) (system.Metrics, error) {
	return system.Collect(), nil
}

// silence unused import if volume isn't otherwise referenced
var _ = volume.Volume{}

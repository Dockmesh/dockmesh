package host

import (
	"context"
	"io"
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
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ListContainers(ctx, all)
}

func (h *LocalHost) InspectContainer(ctx context.Context, id string) (dtypes.ContainerJSON, error) {
	if h.cli == nil {
		return dtypes.ContainerJSON{}, ErrNoDocker
	}
	return h.cli.InspectContainer(ctx, id)
}

func (h *LocalHost) StartContainer(ctx context.Context, id string) error {
	if h.cli == nil {
		return ErrNoDocker
	}
	return h.cli.StartContainer(ctx, id)
}

func (h *LocalHost) StopContainer(ctx context.Context, id string) error {
	if h.cli == nil {
		return ErrNoDocker
	}
	return h.cli.StopContainer(ctx, id)
}

func (h *LocalHost) RestartContainer(ctx context.Context, id string) error {
	if h.cli == nil {
		return ErrNoDocker
	}
	return h.cli.RestartContainer(ctx, id)
}

func (h *LocalHost) RemoveContainer(ctx context.Context, id string, force bool) error {
	if h.cli == nil {
		return ErrNoDocker
	}
	return h.cli.RemoveContainer(ctx, id, force)
}

func (h *LocalHost) ContainerLogs(ctx context.Context, id, tail string, follow bool) (io.ReadCloser, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ContainerLogs(ctx, id, tail, follow)
}

func (h *LocalHost) ContainerStats(ctx context.Context, id string) (io.ReadCloser, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ContainerStats(ctx, id)
}

func (h *LocalHost) StartExec(ctx context.Context, id string, cmd []string) (ExecSession, error) {
	if h.cli == nil {
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
	if h.cli == nil {
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
	if h.cli == nil {
		return ErrNoDocker
	}
	return compose.NewService(h.cli, nil).Stop(ctx, name)
}

func (h *LocalHost) StackStatus(ctx context.Context, name string) ([]compose.StatusEntry, error) {
	if h.cli == nil {
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
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ListImages(ctx, all)
}

func (h *LocalHost) RemoveImage(ctx context.Context, id string, force bool) ([]dtypes.ImageDeleteResponseItem, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.RemoveImage(ctx, id, force)
}

func (h *LocalHost) PruneImages(ctx context.Context) (dtypes.ImagesPruneReport, error) {
	if h.cli == nil {
		return dtypes.ImagesPruneReport{}, ErrNoDocker
	}
	return h.cli.PruneImages(ctx)
}

func (h *LocalHost) ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ListNetworks(ctx)
}

func (h *LocalHost) InspectNetwork(ctx context.Context, id string) (dtypes.NetworkResource, error) {
	if h.cli == nil {
		return dtypes.NetworkResource{}, ErrNoDocker
	}
	return h.cli.InspectNetwork(ctx, id)
}

func (h *LocalHost) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	if h.cli == nil {
		return volume.Volume{}, ErrNoDocker
	}
	return h.cli.InspectVolume(ctx, name)
}

func (h *LocalHost) ListVolumes(ctx context.Context) ([]any, error) {
	if h.cli == nil {
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
	if h.cli == nil {
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
	if h.cli == nil {
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

// SystemMetrics reads host-level CPU / memory / disk / uptime via the
// system package. On Linux it reads /proc and statfs; on other platforms
// (dev builds) the system package stub returns zero values so the
// dashboard still renders without crashing.
func (h *LocalHost) SystemMetrics(ctx context.Context) (system.Metrics, error) {
	return system.Collect(), nil
}

// silence unused import if volume isn't otherwise referenced
var _ = volume.Volume{}

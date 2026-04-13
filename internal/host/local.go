package host

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/docker"
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

func (h *LocalHost) ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ListImages(ctx, all)
}

func (h *LocalHost) ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error) {
	if h.cli == nil {
		return nil, ErrNoDocker
	}
	return h.cli.ListNetworks(ctx)
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

// silence unused import if volume isn't otherwise referenced
var _ = volume.Volume{}

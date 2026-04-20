package backup

import (
	"context"
	"io"

	"github.com/dockmesh/dockmesh/internal/host"
)

// NewHostResolverFromRegistry wraps host.Registry so the backup
// executor can look up agents without importing internal/host
// directly in service.go (keeping the DI surface minimal).
func NewHostResolverFromRegistry(reg *host.Registry) hostResolver {
	return &registryResolver{reg: reg}
}

type registryResolver struct {
	reg *host.Registry
}

func (r *registryResolver) Pick(id string) (hostBackupTarget, error) {
	h, err := r.reg.Pick(id)
	if err != nil {
		return nil, err
	}
	return &hostBackupAdapter{h: h}, nil
}

// hostBackupAdapter narrows host.Host down to the two methods the
// backup executor needs. Other host capabilities stay encapsulated.
type hostBackupAdapter struct {
	h host.Host
}

func (a *hostBackupAdapter) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	return a.h.VolumeTar(ctx, name)
}

func (a *hostBackupAdapter) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	return a.h.ContainerExec(ctx, containerID, cmd)
}

package backup

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"

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

// hostBackupAdapter narrows host.Host down to the methods the
// backup executor needs. Other host capabilities stay encapsulated.
type hostBackupAdapter struct {
	h host.Host
}

func (a *hostBackupAdapter) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	return a.h.VolumeTar(ctx, name)
}

// volumeImporter is implemented by hosts behind an agent; the central
// host is handled by the executor's own helper container.
type volumeImporter interface {
	VolumeImport(ctx context.Context, name string, src io.Reader, replace bool) error
}

func (a *hostBackupAdapter) VolumeImport(ctx context.Context, name string, src io.Reader, replace bool) error {
	vi, ok := a.h.(volumeImporter)
	if !ok {
		return fmt.Errorf("host %s can't restore volumes", a.h.ID())
	}
	return vi.VolumeImport(ctx, name, src, replace)
}

func (a *hostBackupAdapter) VolumeInUse(ctx context.Context, name string) ([]string, error) {
	list, err := a.h.ListContainers(ctx, false)
	if err != nil {
		return nil, err
	}
	var users []container.Summary
	for _, c := range list {
		for _, m := range c.Mounts {
			if m.Type == mount.TypeVolume && m.Name == name {
				users = append(users, c)
				break
			}
		}
	}
	return containerNames(users), nil
}

func (a *hostBackupAdapter) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	return a.h.ContainerExec(ctx, containerID, cmd)
}

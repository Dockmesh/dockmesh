package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// ProxyContainerName is the fixed name we use for the managed Caddy
// container so repeated enable/disable cycles don't leave orphans.
const ProxyContainerName = "dockmesh-proxy"

// ProxyImage is the Caddy image we pull if it isn't already local.
const ProxyImage = "caddy:2"

// EnableProxy pulls the Caddy image (if missing), removes any stale
// container, creates a new one with host networking so it can reach any
// localhost port, starts it, and waits for the admin API.
func (s *Service) EnableProxy(ctx context.Context) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()

	// Pull image if we don't have it.
	if _, _, err := cli.ImageInspectWithRaw(ctx, ProxyImage); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("image inspect: %w", err)
		}
		rc, err := cli.ImagePull(ctx, ProxyImage, dtypes.ImagePullOptions{})
		if err != nil {
			return fmt.Errorf("image pull: %w", err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return fmt.Errorf("image pull read: %w", err)
		}
		rc.Close()
	}

	// Remove any stale container with the reserved name.
	if existing, err := cli.ContainerInspect(ctx, ProxyContainerName); err == nil {
		_ = cli.ContainerStop(ctx, existing.ID, container.StopOptions{})
		if err := cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove stale: %w", err)
		}
	}

	labels := map[string]string{
		"dockmesh.managed":   "true",
		"dockmesh.component": "proxy",
	}
	cfg := &container.Config{
		Image: ProxyImage,
		Cmd:   []string{"caddy", "run", "--config", "/config/caddy.json", "--adapter", "json", "--resume"},
		Labels: labels,
	}
	hostCfg := &container.HostConfig{
		NetworkMode:   "host",
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		Binds: []string{
			"dockmesh-caddy-data:/data",
			"dockmesh-caddy-config:/config",
		},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, &dnetwork.NetworkingConfig{}, nil, ProxyContainerName)
	if err != nil {
		return fmt.Errorf("container create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("container start: %w", err)
	}

	// Wait for admin API (host networking means 127.0.0.1:2019 is Caddy's).
	if err := ensureAdmin(ctx); err != nil {
		return err
	}
	// Push the current routes so the container boots into the right state.
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return err
	}
	return s.pushConfig(ctx, routes)
}

// DisableProxy stops and removes the managed Caddy container. The Docker
// volumes (dockmesh-caddy-data, dockmesh-caddy-config) are left intact so
// reissued certificates survive the disable/enable cycle.
func (s *Service) DisableProxy(ctx context.Context) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, ProxyContainerName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	_ = cli.ContainerStop(ctx, info.ID, container.StopOptions{})
	return cli.ContainerRemove(ctx, info.ID, container.RemoveOptions{Force: true})
}

// GetStatus inspects the container and pings the admin API.
func (s *Service) GetStatus(ctx context.Context) *Status {
	st := &Status{Enabled: s.enabled}
	if s.docker == nil {
		return st
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, ProxyContainerName)
	if err != nil {
		return st
	}
	st.Container = info.ID[:12]
	st.Running = info.State != nil && info.State.Running
	if st.Running {
		ok, server := adminStatus(ctx)
		st.AdminOK = ok
		if server != "" {
			st.Version = server
		}
	}
	return st
}

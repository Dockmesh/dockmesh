package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/compose"
)

// newSpec is the container a swap creates in place of the old one.
type newSpec struct {
	cfg     *container.Config
	hostCfg *container.HostConfig
	netCfg  *dnetwork.NetworkingConfig
	// health drives compose.WaitHealthy; nil means "still running after
	// the grace period" is enough.
	health *composetypes.ServiceConfig
}

// swapContainer replaces old with a container built from spec under the
// same name. The old container is renamed aside and stopped, not removed,
// until the new one runs (and passes its healthcheck); any failure before
// that removes the new container and puts the old one back.
func swapContainer(ctx context.Context, cli *client.Client, old *dtypes.ContainerJSON, spec newSpec) (string, error) {
	if old.HostConfig != nil && old.HostConfig.AutoRemove {
		return "", errors.New("container was started with --rm; stopping it would delete it, recreate it without auto-remove first")
	}
	name := strings.TrimPrefix(old.Name, "/")
	parked := fmt.Sprintf("%s-dmold-%d", name, time.Now().Unix())
	wasRunning := old.State != nil && (old.State.Running || old.State.Paused)

	carryVolumes(old.Mounts, spec.cfg, spec.hostCfg)

	if err := cli.ContainerRename(ctx, old.ID, parked); err != nil {
		return "", fmt.Errorf("rename old container: %w", err)
	}
	restore := func(cause error) error {
		// The caller's ctx may be what failed; putting the old container
		// back must still happen.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := cli.ContainerRename(rctx, old.ID, name); err != nil {
			return fmt.Errorf("%w; restoring the old container failed too, it is kept as %s: %w", cause, parked, err)
		}
		if wasRunning {
			if err := cli.ContainerStart(rctx, old.ID, container.StartOptions{}); err != nil {
				return fmt.Errorf("%w; old container restored but not restarted: %w", cause, err)
			}
		}
		return cause
	}
	discard := func(id string) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := cli.ContainerRemove(rctx, id, container.RemoveOptions{Force: true}); err != nil {
			slog.Warn("updater: remove failed replacement", "container", id, "err", err)
		}
	}

	if wasRunning {
		if err := cli.ContainerStop(ctx, old.ID, container.StopOptions{}); err != nil {
			return "", restore(fmt.Errorf("stop old container: %w", err))
		}
	}
	resp, err := cli.ContainerCreate(ctx, spec.cfg, spec.hostCfg, spec.netCfg, nil, name)
	if err != nil {
		return "", restore(fmt.Errorf("create: %w", err))
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		discard(resp.ID)
		return "", restore(fmt.Errorf("start: %w", err))
	}
	if err := compose.WaitHealthy(ctx, cli, resp.ID, spec.health); err != nil {
		discard(resp.ID)
		return "", restore(fmt.Errorf("new container did not come up: %w", err))
	}
	if err := cli.ContainerRemove(ctx, old.ID, container.RemoveOptions{}); err != nil {
		slog.Warn("updater: old container left behind", "container", parked, "err", err)
	}
	return resp.ID, nil
}

// carryVolumes makes the new container reuse the old one's volumes at
// every mount point the new config doesn't already bind by name.
// Without it, anonymous volumes (`-v /data`, image VOLUME, compose
// `- /data`) are replaced by fresh empty ones on every recreate.
func carryVolumes(old []dtypes.MountPoint, cfg *container.Config, hostCfg *container.HostConfig) {
	bound := make(map[string]bool)
	for _, b := range hostCfg.Binds {
		if dst := bindTarget(b); dst != "" {
			bound[dst] = true
		}
	}
	anon := make(map[string]int)
	for i, m := range hostCfg.Mounts {
		if m.Type == mount.TypeVolume && m.Source == "" {
			anon[m.Target] = i
			continue
		}
		bound[m.Target] = true
	}
	for _, m := range old {
		if m.Type != mount.TypeVolume || m.Name == "" || bound[m.Destination] {
			continue
		}
		if i, ok := anon[m.Destination]; ok {
			hostCfg.Mounts[i].Source = m.Name
			continue
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   m.Name,
			Target:   m.Destination,
			ReadOnly: !m.RW,
		})
		delete(cfg.Volumes, m.Destination)
		bound[m.Destination] = true
	}
}

// bindTarget returns the container path of a `src:dst[:opts]` bind.
func bindTarget(b string) string {
	parts := strings.Split(b, ":")
	if len(parts) < 2 {
		return ""
	}
	// Windows sources carry a drive letter (`C:\x:/dst`).
	if len(parts) >= 3 && len(parts[0]) == 1 && strings.HasPrefix(parts[1], `\`) {
		return parts[2]
	}
	return parts[1]
}

// healthFromConfig lets WaitHealthy honour a healthcheck that lives on
// the container (or its image) rather than in a compose file.
func healthFromConfig(hc *container.HealthConfig) *composetypes.ServiceConfig {
	if hc == nil || len(hc.Test) == 0 || hc.Test[0] == "NONE" {
		return nil
	}
	out := &composetypes.HealthCheckConfig{Test: hc.Test}
	if hc.Interval > 0 {
		d := composetypes.Duration(hc.Interval)
		out.Interval = &d
	}
	if hc.StartPeriod > 0 {
		d := composetypes.Duration(hc.StartPeriod)
		out.StartPeriod = &d
	}
	if hc.Retries > 0 {
		r := uint64(hc.Retries)
		out.Retries = &r
	}
	return &composetypes.ServiceConfig{HealthCheck: out}
}

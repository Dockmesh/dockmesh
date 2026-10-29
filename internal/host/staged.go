package host

import (
	"context"
	"log/slog"
	"sort"

	"github.com/dockmesh/dockmesh/internal/compose"
)

// StagedMount names a stack whose containers still use a bind mount from a
// deploy staging directory of an earlier version.
type StagedMount struct {
	HostID   string
	HostName string
	Stack    string
}

// StagedMountStacks scans every online host for containers with a bind
// mount into an old staging directory. Such a stack lost its relative bind
// mounts and needs a redeploy. Hosts that can't be listed are skipped.
func (r *Registry) StagedMountStacks(ctx context.Context) ([]StagedMount, error) {
	infos, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[StagedMount]bool{}
	for _, info := range infos {
		if info.Status != "online" {
			continue
		}
		h, err := r.Pick(info.ID)
		if err != nil {
			continue
		}
		containers, err := h.ListContainers(ctx, true)
		if err != nil {
			slog.Warn("staged mount scan", "host", info.Name, "err", err)
			continue
		}
		for _, c := range containers {
			stack := c.Labels["com.docker.compose.project"]
			if stack == "" {
				continue
			}
			for _, m := range c.Mounts {
				if m.Type == "bind" && compose.IsStagingPath(m.Source) {
					seen[StagedMount{HostID: info.ID, HostName: info.Name, Stack: stack}] = true
				}
			}
		}
	}
	out := make([]StagedMount, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		return out[i].Stack < out[j].Stack
	})
	return out, nil
}

// DeployHook is called after every successful stack deploy on any host.
type DeployHook func(ctx context.Context, hostID, stack string, res *compose.DeployResult)

// SetAfterDeploy installs a hook that runs after successful deploys.
func (r *Registry) SetAfterDeploy(f DeployHook) {
	r.afterDeploy = f
	r.local.afterDeploy = f
}

package stacks

import (
	"context"
	"log/slog"
	"strings"
)

// ContainerInfo is the minimal container record needed for backfill.
// Avoids importing the docker types package in the stacks package.
type ContainerInfo struct {
	Labels map[string]string
	HostID string
}

// BackfillDeployments detects existing stack → host associations from
// running containers and populates the stack_deployments table for
// stacks that don't have a row yet. Called once at startup after
// migration — see cmd/dockmesh/main.go.
//
// containers should be the union of containers across all hosts, each
// tagged with its HostID. The caller is responsible for the fan-out
// (the stacks package can't import the host package).
//
// Logic per stack:
//   - Group containers by com.docker.compose.project label.
//   - Match each project name against known stacks from the filesystem.
//   - Skip stacks that already have a deployment row.
//   - For stacks with containers on exactly one host → insert (deployed).
//   - For stacks with containers on multiple hosts → pick the host with
//     the most containers, log a warning.
func BackfillDeployments(ctx context.Context, ds *DeploymentStore, mgr *Manager, containers []ContainerInfo) error {
	existing, err := ds.All(ctx)
	if err != nil {
		return err
	}

	// Known stacks from filesystem.
	fsList := mgr.List()
	known := make(map[string]bool, len(fsList))
	for _, s := range fsList {
		known[s.Name] = true
	}

	// Group containers by compose project → host → count.
	type hostCount struct {
		hostID string
		count  int
	}
	projectHosts := make(map[string]map[string]int) // project → hostID → count
	for _, c := range containers {
		proj := c.Labels["com.docker.compose.project"]
		if proj == "" {
			continue
		}
		// Normalise: compose project names are lowercased slugs, same as
		// our stack names. But just in case, lowercase.
		proj = strings.ToLower(proj)
		if !known[proj] {
			continue
		}
		if projectHosts[proj] == nil {
			projectHosts[proj] = make(map[string]int)
		}
		projectHosts[proj][c.HostID]++
	}

	var filled int
	for proj, hosts := range projectHosts {
		if _, exists := existing[proj]; exists {
			continue // already has a deployment row
		}
		// Pick the host with the most containers.
		var best string
		var bestCount int
		for hid, cnt := range hosts {
			if cnt > bestCount {
				best = hid
				bestCount = cnt
			}
		}
		if len(hosts) > 1 {
			slog.Warn("backfill: stack has containers on multiple hosts — picking majority",
				"stack", proj, "host", best, "hosts", len(hosts))
		}
		if err := ds.Set(ctx, proj, best, "deployed"); err != nil {
			slog.Warn("backfill: failed to set deployment", "stack", proj, "err", err)
			continue
		}
		filled++
	}
	if filled > 0 {
		slog.Info("backfill: populated stack deployments", "count", filled)
	}
	return nil
}

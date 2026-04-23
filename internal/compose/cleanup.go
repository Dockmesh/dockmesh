package compose

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"
)

// CleanupOpts picks which project-scoped docker resources to remove for
// a given stack. Containers are stopped/removed by Stop(); Cleanup only
// touches networks, volumes, and images. Kept as independent flags
// because their safety profiles are different:
//   - Networks: project-scoped, removing them is safe (leaked otherwise).
//   - Volumes: user data. Opt-in only. external volumes are never touched.
//   - Images: often shared. Opt-in, and we skip any image still in use
//     by containers outside this project.
type CleanupOpts struct {
	Networks bool
	Volumes  bool
	Images   bool
}

// CleanupPlan lists what Cleanup would remove if invoked with the same
// options — used to show a preview in the UI before the user confirms.
// External/in-use resources are already filtered out.
type CleanupPlan struct {
	Networks        []string `json:"networks"`
	Volumes         []string `json:"volumes"`
	Images          []string `json:"images"`
	SkippedExternal []string `json:"skipped_external,omitempty"` // e.g. "volume:data (external)"
	SkippedInUse    []string `json:"skipped_in_use,omitempty"`   // e.g. "image:nginx:latest (used by other projects)"
}

// CleanupResult mirrors CleanupPlan but with what actually got removed.
type CleanupResult struct {
	Networks []string `json:"networks"`
	Volumes  []string `json:"volumes"`
	Images   []string `json:"images"`
	Errors   []string `json:"errors,omitempty"`
}

// CleanupPreview enumerates the resources Cleanup would touch for a
// project. The resulting plan is scoped to the project via the compose
// labels, and external/shared resources are filtered out the same way
// Cleanup itself would.
func (s *Service) CleanupPreview(ctx context.Context, stackName string) (*CleanupPlan, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	plan := &CleanupPlan{}

	// Networks labelled with this project.
	netFilter := filters.NewArgs()
	netFilter.Add("label", LabelProject+"="+stackName)
	nets, err := cli.NetworkList(ctx, dtypes.NetworkListOptions{Filters: netFilter})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets {
		plan.Networks = append(plan.Networks, n.Name)
	}

	// Volumes labelled with this project. External volumes (no managed
	// label, or external=true) never get touched.
	volFilter := filters.NewArgs()
	volFilter.Add("label", LabelProject+"="+stackName)
	vols, err := cli.VolumeList(ctx, volume.ListOptions{Filters: volFilter})
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range vols.Volumes {
		if v == nil {
			continue
		}
		plan.Volumes = append(plan.Volumes, v.Name)
	}

	// Images referenced by this project's containers. An image is
	// considered shared (and skipped) if any container OUTSIDE this
	// project references it.
	projContainers, err := listProjectContainers(ctx, cli, stackName, true)
	if err != nil {
		return nil, fmt.Errorf("list project containers: %w", err)
	}
	imageSet := map[string]struct{}{}
	for _, c := range projContainers {
		if c.Image != "" {
			imageSet[c.Image] = struct{}{}
		}
		if c.ImageID != "" {
			imageSet[c.ImageID] = struct{}{}
		}
	}
	if len(imageSet) > 0 {
		allContainers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return nil, fmt.Errorf("list containers: %w", err)
		}
		inUseElsewhere := map[string]struct{}{}
		for _, c := range allContainers {
			if strings.EqualFold(c.Labels[LabelProject], stackName) {
				continue
			}
			if _, ok := imageSet[c.Image]; ok {
				inUseElsewhere[c.Image] = struct{}{}
			}
			if _, ok := imageSet[c.ImageID]; ok {
				inUseElsewhere[c.ImageID] = struct{}{}
			}
		}
		for img := range imageSet {
			if _, busy := inUseElsewhere[img]; busy {
				plan.SkippedInUse = append(plan.SkippedInUse, "image:"+img+" (used by other containers)")
				continue
			}
			plan.Images = append(plan.Images, img)
		}
	}

	sort.Strings(plan.Networks)
	sort.Strings(plan.Volumes)
	sort.Strings(plan.Images)
	sort.Strings(plan.SkippedInUse)
	return plan, nil
}

// Cleanup removes the project-scoped networks/volumes/images selected in
// opts. Containers must already be stopped (via Stop); this method does
// not touch containers. Missing resources are treated as already-cleaned
// and do not fail the call.
func (s *Service) Cleanup(ctx context.Context, stackName string, opts CleanupOpts) (*CleanupResult, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	res := &CleanupResult{}

	if opts.Networks {
		netFilter := filters.NewArgs()
		netFilter.Add("label", LabelProject+"="+stackName)
		nets, err := cli.NetworkList(ctx, dtypes.NetworkListOptions{Filters: netFilter})
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		for _, n := range nets {
			if err := cli.NetworkRemove(ctx, n.ID); err != nil && !errdefs.IsNotFound(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("network %s: %v", n.Name, err))
				continue
			}
			res.Networks = append(res.Networks, n.Name)
		}
	}

	if opts.Volumes {
		volFilter := filters.NewArgs()
		volFilter.Add("label", LabelProject+"="+stackName)
		vols, err := cli.VolumeList(ctx, volume.ListOptions{Filters: volFilter})
		if err != nil {
			return nil, fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range vols.Volumes {
			if v == nil {
				continue
			}
			if err := cli.VolumeRemove(ctx, v.Name, false); err != nil && !errdefs.IsNotFound(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("volume %s: %v", v.Name, err))
				continue
			}
			res.Volumes = append(res.Volumes, v.Name)
		}
	}

	if opts.Images {
		// Recompute the same plan Preview would produce — safer than
		// trusting a stale plan the caller passed in.
		plan, err := s.CleanupPreview(ctx, stackName)
		if err != nil {
			return nil, fmt.Errorf("image preview: %w", err)
		}
		for _, img := range plan.Images {
			if _, err := cli.ImageRemove(ctx, img, dtypes.ImageRemoveOptions{PruneChildren: true}); err != nil && !errdefs.IsNotFound(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("image %s: %v", img, err))
				continue
			}
			res.Images = append(res.Images, img)
		}
	}

	return res, nil
}

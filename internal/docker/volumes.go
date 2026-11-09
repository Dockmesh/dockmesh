package docker

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/api/types/volume"
)

// ErrInvalidVolumeName is returned by CheckVolumeName.
var ErrInvalidVolumeName = errors.New("invalid volume name")

// volumeNamePattern is Docker's own rule for volume names.
var volumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

// CheckVolumeName rejects anything that isn't a Docker volume name. A
// "<source>:/target" bind whose source is a path mounts that host path, so
// every bind built from user, job or archive input must pass this first.
func CheckVolumeName(name string) error {
	if !volumeNamePattern.MatchString(name) {
		return fmt.Errorf("%w %q", ErrInvalidVolumeName, name)
	}
	return nil
}

func (c *Client) ListVolumes(ctx context.Context) ([]*volume.Volume, error) {
	resp, err := c.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	return resp.Volumes, nil
}

func (c *Client) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	return c.cli.VolumeInspect(ctx, name)
}

func (c *Client) CreateVolume(ctx context.Context, name, driver string, labels map[string]string) (volume.Volume, error) {
	if driver == "" {
		driver = "local"
	}
	return c.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   name,
		Driver: driver,
		Labels: labels,
	})
}

func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	return c.cli.VolumeRemove(ctx, name, force)
}

func (c *Client) PruneVolumes(ctx context.Context) (volume.PruneReport, error) {
	return PruneUnusedVolumes(ctx, c.cli)
}

// LabelManaged marks resources Dockmesh created.
const LabelManaged = "dockmesh.managed"

// anonymousPruneAPI is the first Docker API whose volume prune leaves
// named volumes alone unless asked otherwise.
const anonymousPruneAPI = "1.42"

var anonymousVolumeName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// volumePruner is the part of the Docker client PruneUnusedVolumes uses.
type volumePruner interface {
	ServerVersion(ctx context.Context) (types.Version, error)
	ClientVersion() string
	VolumesPrune(ctx context.Context, f filters.Args) (volume.PruneReport, error)
	VolumeList(ctx context.Context, opts volume.ListOptions) (volume.ListResponse, error)
	VolumeRemove(ctx context.Context, id string, force bool) error
}

// PruneUnusedVolumes removes anonymous volumes no container uses and
// keeps named volumes and Dockmesh's own.
func PruneUnusedVolumes(ctx context.Context, cli volumePruner) (volume.PruneReport, error) {
	v, err := cli.ServerVersion(ctx)
	if err != nil {
		return volume.PruneReport{}, err
	}
	// The daemon goes by the request's version, which DOCKER_API_VERSION
	// can pin below what the daemon supports.
	if versions.LessThan(v.APIVersion, anonymousPruneAPI) || versions.LessThan(cli.ClientVersion(), anonymousPruneAPI) {
		return pruneAnonymousByName(ctx, cli)
	}
	return cli.VolumesPrune(ctx, filters.NewArgs(filters.Arg("label!", LabelManaged)))
}

// pruneAnonymousByName is for daemons whose prune also deletes unused
// named volumes, including stopped stacks' data. They have no anonymous
// label either, so the generated name is what identifies such a volume.
func pruneAnonymousByName(ctx context.Context, cli volumePruner) (volume.PruneReport, error) {
	report := volume.PruneReport{VolumesDeleted: []string{}}
	list, err := cli.VolumeList(ctx, volume.ListOptions{Filters: filters.NewArgs(filters.Arg("dangling", "true"))})
	if err != nil {
		return report, err
	}
	for _, v := range list.Volumes {
		if v == nil || !anonymousVolumeName.MatchString(v.Name) || v.Labels[LabelManaged] == "true" {
			continue
		}
		// Docker's own prune doesn't touch plugin-backed volumes either.
		if v.Driver != "local" || len(v.Options) > 0 {
			continue
		}
		if err := cli.VolumeRemove(ctx, v.Name, false); err != nil {
			if cerrdefs.IsConflict(err) || cerrdefs.IsNotFound(err) {
				continue
			}
			return report, fmt.Errorf("remove volume %s: %w", v.Name, err)
		}
		report.VolumesDeleted = append(report.VolumesDeleted, v.Name)
	}
	return report, nil
}

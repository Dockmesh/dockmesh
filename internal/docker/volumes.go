package docker

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
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

func (c *Client) ListVolumes(ctx context.Context) ([]volume.Volume, error) {
	resp, err := c.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *Client) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	res, err := c.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	return res.Volume, err
}

func (c *Client) CreateVolume(ctx context.Context, name, driver string, labels map[string]string) (volume.Volume, error) {
	if driver == "" {
		driver = "local"
	}
	res, err := c.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   name,
		Driver: driver,
		Labels: labels,
	})
	return res.Volume, err
}

func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	_, err := c.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return err
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
	ServerVersion(ctx context.Context, opts client.ServerVersionOptions) (client.ServerVersionResult, error)
	ClientVersion() string
	VolumePrune(ctx context.Context, opts client.VolumePruneOptions) (client.VolumePruneResult, error)
	VolumeList(ctx context.Context, opts client.VolumeListOptions) (client.VolumeListResult, error)
	VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

// PruneUnusedVolumes removes anonymous volumes no container uses and
// keeps named volumes and Dockmesh's own.
func PruneUnusedVolumes(ctx context.Context, cli volumePruner) (volume.PruneReport, error) {
	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return volume.PruneReport{}, err
	}
	// The daemon goes by the request's version, which DOCKER_API_VERSION
	// can pin below what the daemon supports.
	if versions.LessThan(v.APIVersion, anonymousPruneAPI) || versions.LessThan(cli.ClientVersion(), anonymousPruneAPI) {
		return pruneAnonymousByName(ctx, cli)
	}
	res, err := cli.VolumePrune(ctx, client.VolumePruneOptions{Filters: client.Filters{}.Add("label!", LabelManaged)})
	return res.Report, err
}

// pruneAnonymousByName is for daemons whose prune also deletes unused
// named volumes, including stopped stacks' data. They have no anonymous
// label either, so the generated name is what identifies such a volume.
func pruneAnonymousByName(ctx context.Context, cli volumePruner) (volume.PruneReport, error) {
	report := volume.PruneReport{VolumesDeleted: []string{}}
	list, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("dangling", "true")})
	if err != nil {
		return report, err
	}
	for _, v := range list.Items {
		if !anonymousVolumeName.MatchString(v.Name) || v.Labels[LabelManaged] == "true" {
			continue
		}
		// Docker's own prune doesn't touch plugin-backed volumes either.
		if v.Driver != "local" || len(v.Options) > 0 {
			continue
		}
		if _, err := cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{}); err != nil {
			if cerrdefs.IsConflict(err) || cerrdefs.IsNotFound(err) {
				continue
			}
			return report, fmt.Errorf("remove volume %s: %w", v.Name, err)
		}
		report.VolumesDeleted = append(report.VolumesDeleted, v.Name)
	}
	return report, nil
}

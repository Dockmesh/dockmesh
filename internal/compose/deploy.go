package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/stacks"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
)

// Project labels applied to every resource we create so Stop/Status
// can find them back without a DB entry.
const (
	LabelProject = "com.docker.compose.project"
	LabelService = "com.docker.compose.service"
	LabelManaged = "dockmesh.managed"
)

// Service wires a Docker client and stack manager into compose operations.
//
// Supported compose features in Phase 1:
//   - services: image, environment, command, entrypoint, ports, volumes,
//     networks, restart, labels, user, working_dir, hostname, privileged,
//     read_only, cap_add, cap_drop, tty, stdin_open
//   - top-level networks/volumes (named, non-external → auto-create; external
//     assumed to exist)
//   - .env file resolution
//
// Explicitly NOT supported yet (will be ignored or error out clearly):
//   - build:, secrets:, configs:, depends_on ordering, healthcheck, deploy:,
//     profiles, extends, network aliases, ipam overrides
type Service struct {
	docker *docker.Client
	stacks *stacks.Manager
}

func NewService(d *docker.Client, s *stacks.Manager) *Service {
	return &Service{docker: d, stacks: s}
}

type DeployResult struct {
	Stack    string           `json:"stack"`
	Services []ServiceResult  `json:"services"`
	Networks []ResourceResult `json:"networks,omitempty"`
	Volumes  []ResourceResult `json:"volumes,omitempty"`
}

type ServiceResult struct {
	Name        string `json:"name"`
	ContainerID string `json:"container_id"`
	Image       string `json:"image"`
}

type ResourceResult struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
}

type StatusEntry struct {
	Service     string `json:"service"`
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Image       string `json:"image"`
}

func (s *Service) Deploy(ctx context.Context, stackName string) (*DeployResult, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	dir, err := s.stacks.Dir(stackName)
	if err != nil {
		return nil, err
	}
	// Fetch the decrypted env through the stack manager — this is the
	// only place plaintext secrets exist, and it stays in memory.
	detail, err := s.stacks.Get(stackName)
	if err != nil {
		return nil, err
	}
	proj, err := LoadProject(ctx, dir, stackName, detail.Env)
	if err != nil {
		return nil, err
	}

	cli := s.docker.Raw()
	result := &DeployResult{Stack: stackName}

	netNames, err := s.reconcileNetworks(ctx, cli, proj, result)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileVolumes(ctx, cli, proj, result); err != nil {
		return nil, err
	}

	// Deterministic order — we do not yet resolve depends_on.
	names := make([]string, 0, len(proj.Services))
	for n := range proj.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		svc := proj.Services[name]
		sr, err := s.deployService(ctx, cli, proj, svc, netNames)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		result.Services = append(result.Services, *sr)
	}
	return result, nil
}

func (s *Service) Stop(ctx context.Context, stackName string) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	list, err := listProjectContainers(ctx, cli, stackName, true)
	if err != nil {
		return err
	}
	for _, c := range list {
		_ = cli.ContainerStop(ctx, c.ID, container.StopOptions{})
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

func (s *Service) Status(ctx context.Context, stackName string) ([]StatusEntry, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	list, err := listProjectContainers(ctx, s.docker.Raw(), stackName, true)
	if err != nil {
		return nil, err
	}
	out := make([]StatusEntry, 0, len(list))
	for _, c := range list {
		out = append(out, StatusEntry{
			Service:     c.Labels[LabelService],
			ContainerID: c.ID,
			State:       c.State,
			Status:      c.Status,
			Image:       c.Image,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}

// -----------------------------------------------------------------------------
// reconcile helpers
// -----------------------------------------------------------------------------

// reconcileNetworks creates project-scoped networks that don't exist yet and
// returns a map of compose-network-key → actual docker network name.
func (s *Service) reconcileNetworks(ctx context.Context, cli *client.Client, proj *composetypes.Project, res *DeployResult) (map[string]string, error) {
	out := make(map[string]string, len(proj.Networks))
	for key, net := range proj.Networks {
		actualName := net.Name
		if actualName == "" {
			actualName = proj.Name + "_" + key
		}
		out[key] = actualName

		if bool(net.External) {
			continue
		}
		if _, err := cli.NetworkInspect(ctx, actualName, dtypes.NetworkInspectOptions{}); err == nil {
			res.Networks = append(res.Networks, ResourceResult{Name: actualName, Created: false})
			continue
		} else if !errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("network inspect %s: %w", actualName, err)
		}

		labels := map[string]string{
			LabelProject: proj.Name,
			LabelManaged: "true",
		}
		for k, v := range net.Labels {
			labels[k] = v
		}
		driver := net.Driver
		if driver == "" {
			driver = "bridge"
		}
		if _, err := cli.NetworkCreate(ctx, actualName, dtypes.NetworkCreate{
			Driver:     driver,
			Internal:   net.Internal,
			Attachable: net.Attachable,
			Labels:     labels,
			Options:    net.DriverOpts,
		}); err != nil {
			return nil, fmt.Errorf("network create %s: %w", actualName, err)
		}
		res.Networks = append(res.Networks, ResourceResult{Name: actualName, Created: true})
	}
	return out, nil
}

func (s *Service) reconcileVolumes(ctx context.Context, cli *client.Client, proj *composetypes.Project, res *DeployResult) error {
	for key, vol := range proj.Volumes {
		actualName := vol.Name
		if actualName == "" {
			actualName = proj.Name + "_" + key
		}
		if bool(vol.External) {
			continue
		}
		if _, err := cli.VolumeInspect(ctx, actualName); err == nil {
			res.Volumes = append(res.Volumes, ResourceResult{Name: actualName, Created: false})
			continue
		} else if !errdefs.IsNotFound(err) {
			return fmt.Errorf("volume inspect %s: %w", actualName, err)
		}

		labels := map[string]string{
			LabelProject: proj.Name,
			LabelManaged: "true",
		}
		for k, v := range vol.Labels {
			labels[k] = v
		}
		driver := vol.Driver
		if driver == "" {
			driver = "local"
		}
		if _, err := cli.VolumeCreate(ctx, volume.CreateOptions{
			Name:       actualName,
			Driver:     driver,
			DriverOpts: vol.DriverOpts,
			Labels:     labels,
		}); err != nil {
			return fmt.Errorf("volume create %s: %w", actualName, err)
		}
		res.Volumes = append(res.Volumes, ResourceResult{Name: actualName, Created: true})
	}
	return nil
}

// -----------------------------------------------------------------------------
// per-service deployment
// -----------------------------------------------------------------------------

func (s *Service) deployService(ctx context.Context, cli *client.Client, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string) (*ServiceResult, error) {
	if svc.Image == "" {
		return nil, errors.New("image is required (build: not supported in phase 1)")
	}

	containerName := svc.ContainerName
	if containerName == "" {
		containerName = fmt.Sprintf("%s-%s-1", proj.Name, svc.Name)
	}

	// Remove any existing container with the same name (idempotent redeploy).
	if existing, err := cli.ContainerInspect(ctx, containerName); err == nil {
		_ = cli.ContainerStop(ctx, existing.ID, container.StopOptions{})
		if err := cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("remove existing %s: %w", containerName, err)
		}
	}

	// Ensure image is present locally.
	if _, _, err := cli.ImageInspectWithRaw(ctx, svc.Image); err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("image inspect %s: %w", svc.Image, err)
		}
		rc, err := cli.ImagePull(ctx, svc.Image, dtypes.ImagePullOptions{})
		if err != nil {
			return nil, fmt.Errorf("image pull %s: %w", svc.Image, err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return nil, fmt.Errorf("image pull read %s: %w", svc.Image, err)
		}
		rc.Close()
	}

	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
	if err != nil {
		return nil, err
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, containerName)
	if err != nil {
		return nil, fmt.Errorf("container create %s: %w", containerName, err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("container start %s: %w", containerName, err)
	}
	return &ServiceResult{Name: svc.Name, ContainerID: resp.ID, Image: svc.Image}, nil
}

func serviceToContainerConfig(proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string) (*container.Config, *container.HostConfig, *dnetwork.NetworkingConfig, error) {
	// Environment: compose uses MappingWithEquals (map[string]*string); nil means "inherit".
	env := make([]string, 0, len(svc.Environment))
	for k, v := range svc.Environment {
		if v == nil {
			continue
		}
		env = append(env, k+"="+*v)
	}
	sort.Strings(env)

	// Ports.
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range svc.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		port, err := nat.NewPort(proto, fmt.Sprintf("%d", p.Target))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("port %d/%s: %w", p.Target, proto, err)
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], nat.PortBinding{
			HostIP:   p.HostIP,
			HostPort: p.Published,
		})
	}

	// Volumes → split into bind strings and named-volume mounts.
	var binds []string
	var mounts []mount.Mount
	for _, v := range svc.Volumes {
		switch v.Type {
		case composetypes.VolumeTypeBind, "":
			// Default short form is already bind. compose-go sets Type="".
			if v.Source == "" {
				continue
			}
			bind := fmt.Sprintf("%s:%s", v.Source, v.Target)
			if v.ReadOnly {
				bind += ":ro"
			}
			binds = append(binds, bind)
		case composetypes.VolumeTypeVolume:
			source := v.Source
			if vol, ok := proj.Volumes[v.Source]; ok {
				if vol.Name != "" {
					source = vol.Name
				} else {
					source = proj.Name + "_" + v.Source
				}
			}
			mounts = append(mounts, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   source,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})
		case composetypes.VolumeTypeTmpfs:
			mounts = append(mounts, mount.Mount{Type: mount.TypeTmpfs, Target: v.Target})
		default:
			return nil, nil, nil, fmt.Errorf("unsupported volume type %q", v.Type)
		}
	}

	// Labels — merge user labels with our tracking labels.
	labels := map[string]string{
		LabelProject: proj.Name,
		LabelService: svc.Name,
		LabelManaged: "true",
	}
	for k, v := range svc.Labels {
		labels[k] = v
	}

	cfg := &container.Config{
		Image:        svc.Image,
		Env:          env,
		Cmd:          []string(svc.Command),
		Entrypoint:   []string(svc.Entrypoint),
		Labels:       labels,
		ExposedPorts: exposed,
		User:         svc.User,
		WorkingDir:   svc.WorkingDir,
		Hostname:     svc.Hostname,
		Tty:          svc.Tty,
		OpenStdin:    svc.StdinOpen,
	}

	hostCfg := &container.HostConfig{
		PortBindings: bindings,
		Binds:        binds,
		Mounts:       mounts,
		Privileged:   svc.Privileged,
		ReadonlyRootfs: svc.ReadOnly,
		CapAdd:       svc.CapAdd,
		CapDrop:      svc.CapDrop,
	}
	if svc.Restart != "" {
		hostCfg.RestartPolicy = container.RestartPolicy{
			Name: container.RestartPolicyMode(svc.Restart),
		}
	}
	if svc.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(svc.NetworkMode)
	}

	netCfg := &dnetwork.NetworkingConfig{}
	if len(svc.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*dnetwork.EndpointSettings, len(svc.Networks))
		// Deterministic — pick lowest-priority-sort network for the initial
		// attach; additional networks would need a post-create connect which
		// we'll add later.
		keys := svc.NetworksByPriority()
		for _, k := range keys {
			actual, ok := netNames[k]
			if !ok {
				actual = proj.Name + "_" + k
			}
			endpoint := &dnetwork.EndpointSettings{}
			if nc := svc.Networks[k]; nc != nil && len(nc.Aliases) > 0 {
				endpoint.Aliases = nc.Aliases
			}
			netCfg.EndpointsConfig[actual] = endpoint
		}
	}

	return cfg, hostCfg, netCfg, nil
}

func listProjectContainers(ctx context.Context, cli *client.Client, stackName string, all bool) ([]dtypes.Container, error) {
	f := filters.NewArgs()
	f.Add("label", LabelProject+"="+stackName)
	list, err := cli.ContainerList(ctx, container.ListOptions{All: all, Filters: f})
	if err != nil {
		return nil, err
	}
	// Sanity: filter again locally in case the daemon returns something unexpected.
	out := list[:0]
	for _, c := range list {
		if strings.EqualFold(c.Labels[LabelProject], stackName) {
			out = append(out, c)
		}
	}
	return out, nil
}

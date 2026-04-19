package compose

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// Known database image prefixes — used to warn about scaling stateful
// services that probably can't share a volume across replicas.
var dbImagePrefixes = []string{
	"postgres", "mysql", "mariadb", "mongo", "redis",
	"elasticsearch", "cockroach", "influxdb", "clickhouse",
	"neo4j", "cassandra", "memcached", "etcd",
}

// ScaleCheck is the pre-flight analysis returned by CheckScale so the
// handler / UI can show appropriate warnings before committing.
type ScaleCheck struct {
	Service         string `json:"service"`
	CurrentReplicas int    `json:"current_replicas"`
	// Safety flags — any of these being true should surface a warning
	// or outright refusal at the API layer.
	HasContainerName bool   `json:"has_container_name"`
	HasHardPort      bool   `json:"has_hard_port"`
	HardPortDetail   string `json:"hard_port_detail,omitempty"`
	IsStateful       bool   `json:"is_stateful"`
	StatefulImage    string `json:"stateful_image,omitempty"`
	HasVolumes       bool   `json:"has_volumes"`
}

// ScaleResult is what ScaleService returns after adjusting replica count.
type ScaleResult struct {
	Service  string `json:"service"`
	Previous int    `json:"previous"`
	Current  int    `json:"current"`
	Created  int    `json:"created"`
	Removed  int    `json:"removed"`
}

// CheckScale analyses whether a service can safely be scaled and
// reports the current replica count + any safety flags. The caller
// decides whether to block or warn.
func (s *Service) CheckScale(ctx context.Context, proj *composetypes.Project, serviceName string) (*ScaleCheck, error) {
	svc, ok := proj.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in project %s", serviceName, proj.Name)
	}
	check := &ScaleCheck{Service: serviceName}

	// Current replica count from running containers.
	containers, err := listServiceContainers(ctx, s.docker.Raw(), proj.Name, serviceName)
	if err != nil {
		return nil, err
	}
	check.CurrentReplicas = len(containers)

	// container_name check.
	check.HasContainerName = svc.ContainerName != ""

	// Hard port check — a fixed host port (not a range) means only one
	// container can bind to it.
	for _, p := range svc.Ports {
		if p.Published != "" && !strings.Contains(p.Published, "-") {
			check.HasHardPort = true
			check.HardPortDetail = fmt.Sprintf("%s:%d", p.Published, p.Target)
			break
		}
	}

	// Stateful check — volumes mounted AND image matches a DB pattern.
	check.HasVolumes = len(svc.Volumes) > 0
	img := strings.ToLower(svc.Image)
	for _, prefix := range dbImagePrefixes {
		// Match "postgres", "postgres:15", "docker.io/library/postgres:15-alpine"
		base := img
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		if idx := strings.Index(base, ":"); idx >= 0 {
			base = base[:idx]
		}
		if base == prefix {
			check.IsStateful = true
			check.StatefulImage = prefix
			break
		}
	}

	return check, nil
}

// ScaleService adjusts the replica count for a single service within a
// stack. It adds or removes containers to reach the desired count.
//
// Callers should run CheckScale first and refuse or warn as needed —
// ScaleService itself only enforces hard blocks (container_name, hard
// ports) and trusts the caller for soft warnings (stateful).
func (s *Service) ScaleService(ctx context.Context, proj *composetypes.Project, serviceName string, replicas int) (*ScaleResult, error) {
	if replicas < 0 || replicas > 100 {
		return nil, errors.New("replicas must be between 0 and 100")
	}
	svc, ok := proj.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in project %s", serviceName, proj.Name)
	}
	cli := s.docker.Raw()

	// Hard blocks.
	if replicas > 1 && svc.ContainerName != "" {
		return nil, fmt.Errorf("service %s has container_name set — remove it to allow scaling", serviceName)
	}
	if replicas > 1 {
		for _, p := range svc.Ports {
			if p.Published != "" && !strings.Contains(p.Published, "-") {
				return nil, fmt.Errorf("service %s has hard-coded host port %s:%d — use a port range or remove the host binding to allow scaling",
					serviceName, p.Published, p.Target)
			}
		}
	}

	// Current containers for this service, sorted by replica index.
	existing, err := listServiceContainers(ctx, cli, proj.Name, serviceName)
	if err != nil {
		return nil, err
	}
	sortByReplicaIndex(existing, proj.Name, serviceName)

	current := len(existing)
	result := &ScaleResult{Service: serviceName, Previous: current}

	switch {
	case replicas == current:
		result.Current = current
		return result, nil

	case replicas > current:
		// Scale up: create missing replicas.
		// Find the network names map for container config.
		netNames := make(map[string]string)
		for key, net := range proj.Networks {
			if net.Name != "" {
				netNames[key] = net.Name
			} else {
				netNames[key] = proj.Name + "_" + key
			}
		}
		for i := current + 1; i <= replicas; i++ {
			name := fmt.Sprintf("%s-%s-%d", proj.Name, serviceName, i)
			cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
			if err != nil {
				return nil, fmt.Errorf("config replica %d: %w", i, err)
			}
			// Remove stale container with the same name if it exists.
			if old, inspErr := cli.ContainerInspect(ctx, name); inspErr == nil {
				_ = cli.ContainerStop(ctx, old.ID, container.StopOptions{})
				_ = cli.ContainerRemove(ctx, old.ID, container.RemoveOptions{Force: true})
			}
			resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
			if err != nil {
				return nil, fmt.Errorf("create replica %d: %w", i, err)
			}
			if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
				return nil, fmt.Errorf("start replica %d: %w", i, err)
			}
			// Gate the next replica behind this one becoming healthy —
			// otherwise a crash-looping replica would still tick result.Created++
			// and the scale-up reports success while nothing works.
			if err := WaitHealthy(ctx, cli, resp.ID, &svc); err != nil {
				return nil, fmt.Errorf("replica %d (%s): %w", i, name, err)
			}
			result.Created++
		}

	case replicas < current:
		// Scale down: remove highest-indexed replicas first.
		for i := current - 1; i >= replicas; i-- {
			c := existing[i]
			_ = cli.ContainerStop(ctx, c.ID, container.StopOptions{})
			if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return nil, fmt.Errorf("remove replica %s: %w", c.ID[:12], err)
			}
			result.Removed++
		}
	}

	result.Current = replicas
	return result, nil
}

// listServiceContainers returns containers belonging to a specific
// service within a stack project.
func listServiceContainers(ctx context.Context, cli *client.Client, stackName, serviceName string) ([]dtypes.Container, error) {
	all, err := listProjectContainers(ctx, cli, stackName, true)
	if err != nil {
		return nil, err
	}
	var out []dtypes.Container
	for _, c := range all {
		if c.Labels[LabelService] == serviceName {
			out = append(out, c)
		}
	}
	return out, nil
}

// sortByReplicaIndex sorts containers by their replica index suffix
// (e.g. mystack-web-1 < mystack-web-2 < mystack-web-3).
func sortByReplicaIndex(cs []dtypes.Container, stack, service string) {
	prefix := stack + "-" + service + "-"
	sort.Slice(cs, func(i, j int) bool {
		return replicaIndex(cs[i], prefix) < replicaIndex(cs[j], prefix)
	})
}

func replicaIndex(c dtypes.Container, prefix string) int {
	for _, name := range c.Names {
		name = strings.TrimPrefix(name, "/")
		if strings.HasPrefix(name, prefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(name, prefix)); err == nil {
				return n
			}
		}
	}
	return 999 // unknown → sort last
}

package compose

import (
	"context"
	"fmt"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// ServiceContainerConfig returns the create config a deploy of the
// project would use for one service, so callers that replace a single
// service's containers (the image updater) stay in line with compose.yaml.
func ServiceContainerConfig(proj *composetypes.Project, serviceName string) (*container.Config, *container.HostConfig, *dnetwork.NetworkingConfig, *composetypes.ServiceConfig, error) {
	svc, ok := proj.Services[serviceName]
	if !ok {
		return nil, nil, nil, nil, fmt.Errorf("service %q not found in project %s", serviceName, proj.Name)
	}
	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, projectNetworkNames(proj))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return cfg, hostCfg, netCfg, &svc, nil
}

// ServiceContainers lists every container of one stack service, running
// or not, ordered by replica index.
func ServiceContainers(ctx context.Context, cli *client.Client, stackName, serviceName string) ([]dtypes.Container, error) {
	cs, err := listServiceContainers(ctx, cli, stackName, serviceName)
	if err != nil {
		return nil, err
	}
	sortByReplicaIndex(cs, stackName, serviceName)
	return cs, nil
}

func projectNetworkNames(proj *composetypes.Project) map[string]string {
	names := make(map[string]string, len(proj.Networks))
	for key, net := range proj.Networks {
		actual := net.Name
		if actual == "" {
			actual = proj.Name + "_" + key
		}
		names[key] = actual
	}
	return names
}

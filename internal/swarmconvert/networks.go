package swarmconvert

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

const defaultNetworkDriver = "overlay"

// networkName is the engine name of a compose network, which compose-go
// has set; the implicit default network has no entry in project.Networks.
func networkName(stack, key string, nw types.NetworkConfig) string {
	if nw.Name != "" {
		return nw.Name
	}
	return scoped(stack, key)
}

// isUserDefined mirrors the engine's NetworkMode.IsUserDefined for a
// network name: the predefined local networks get no service alias.
func isUserDefined(name string) bool {
	switch name {
	case "default", "bridge", "host", "none":
		return false
	}
	return !strings.HasPrefix(name, "container:")
}

func convertServiceNetworks(stack string, svc types.ServiceConfig, networks types.Networks) ([]swarm.NetworkAttachmentConfig, error) {
	attached := svc.Networks
	if len(attached) == 0 {
		attached = map[string]*types.ServiceNetworkConfig{defaultNetwork: nil}
	}
	out := make([]swarm.NetworkAttachmentConfig, 0, len(attached))
	for key, cfg := range attached {
		nw, ok := networks[key]
		if !ok && key != defaultNetwork {
			return nil, fmt.Errorf("undefined network %q", key)
		}
		att := swarm.NetworkAttachmentConfig{Target: networkName(stack, key, nw)}
		if cfg != nil {
			att.Aliases = append(att.Aliases, cfg.Aliases...)
			att.DriverOpts = cfg.DriverOpts
		}
		if isUserDefined(att.Target) {
			att.Aliases = append(att.Aliases, svc.Name)
		}
		out = append(out, att)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out, nil
}

// usedNetworks is the set of networks the services attach to; a service
// without networks is on the default one.
func usedNetworks(project *types.Project) map[string]bool {
	used := map[string]bool{}
	for _, svc := range project.Services {
		if len(svc.Networks) == 0 {
			used[defaultNetwork] = true
		}
		for key := range svc.Networks {
			used[key] = true
		}
	}
	return used
}

// convertNetworks returns the create requests for the networks the
// services use, and the names of the external ones the deploy has to
// find on the Swarm. Networks no service uses are not created, as with
// the CLI.
func convertNetworks(stack string, project *types.Project) ([]network.CreateRequest, []string, error) {
	used := usedNetworks(project)
	var creates []network.CreateRequest
	var external []string
	byName := map[string]string{}
	for _, key := range sortedKeys(used) {
		nw := project.Networks[key]
		name := networkName(stack, key, nw)
		if other, dup := byName[name]; dup {
			return nil, nil, fmt.Errorf("networks %s and %s both name the network %s", other, key, name)
		}
		byName[name] = key
		if nw.External {
			external = append(external, name)
			continue
		}
		labels := map[string]string{}
		for k, v := range nw.Labels {
			labels[k] = v
		}
		labels[LabelNamespace] = stack
		req := network.CreateRequest{
			Name:       name,
			Driver:     nw.Driver,
			Options:    nw.DriverOpts,
			Internal:   nw.Internal,
			Attachable: nw.Attachable,
			Labels:     labels,
		}
		if req.Driver == "" {
			req.Driver = defaultNetworkDriver
		}
		if nw.Ipam.Driver != "" || len(nw.Ipam.Config) > 0 {
			req.IPAM = &network.IPAM{Driver: nw.Ipam.Driver}
			for _, pool := range nw.Ipam.Config {
				if pool == nil {
					continue
				}
				sn, err := netip.ParsePrefix(pool.Subnet)
				if err != nil {
					return nil, nil, fmt.Errorf("network %s: subnet %q: %w", key, pool.Subnet, err)
				}
				req.IPAM.Config = append(req.IPAM.Config, network.IPAMConfig{Subnet: sn})
			}
		}
		creates = append(creates, req)
	}
	return creates, external, nil
}

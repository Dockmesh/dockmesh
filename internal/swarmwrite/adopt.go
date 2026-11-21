package swarmwrite

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Export reads the services of a stack by its namespace label, and the
// Swarm networks that carry the label or that one of the services
// attaches to.
func Export(ctx context.Context, cli API, req agents.SwarmExportReq) (agents.SwarmExportResp, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := req.Validate(); err != nil {
		return agents.SwarmExportResp{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmExportResp{}, err
	}
	res, err := cli.ServiceList(ctx, client.ServiceListOptions{
		Filters: client.Filters{}.Add("label", agents.LabelStackNamespace+"="+req.Stack),
	})
	if err != nil {
		return agents.SwarmExportResp{}, fmt.Errorf("list services: %w", err)
	}
	out := agents.SwarmExportResp{Networks: []network.Summary{}}
	attached := map[string]bool{}
	for _, s := range res.Items {
		if s.Spec.Labels[agents.LabelStackNamespace] != req.Stack {
			continue
		}
		out.Services = append(out.Services, s)
		for _, a := range s.Spec.TaskTemplate.Networks {
			attached[a.Target] = true
		}
	}
	if len(out.Services) == 0 {
		return agents.SwarmExportResp{}, fmt.Errorf("%w: no services of stack %s", agents.ErrSwarmWriteRefused, req.Stack)
	}
	if len(out.Services) > agents.MaxSwarmWriteServices {
		return agents.SwarmExportResp{}, fmt.Errorf("%w: stack %s has more than %d services", agents.ErrSwarmWriteRefused, req.Stack, agents.MaxSwarmWriteServices)
	}
	slices.SortFunc(out.Services, func(a, b swarm.Service) int { return strings.Compare(a.Spec.Name, b.Spec.Name) })
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("scope", "swarm")})
	if err != nil {
		return agents.SwarmExportResp{}, fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets.Items {
		if n.Labels[agents.LabelStackNamespace] == req.Stack || attached[n.ID] || attached[n.Name] {
			out.Networks = append(out.Networks, n)
		}
	}
	if len(out.Networks) > agents.MaxSwarmWriteNetworks {
		return agents.SwarmExportResp{}, fmt.Errorf("%w: stack %s uses more than %d networks", agents.ErrSwarmWriteRefused, req.Stack, agents.MaxSwarmWriteNetworks)
	}
	return out, nil
}

// Adopt adds the spec hash label to each named service of the stack and
// changes nothing else, so no task is replaced. Every service is checked
// before the first write: it must carry the stack's namespace label and
// still be at the version the server read. One that already carries a
// spec hash is left as it is.
func Adopt(ctx context.Context, cli API, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := req.Validate(); err != nil {
		return agents.SwarmAdoptResp{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmAdoptResp{}, err
	}
	names := make([]string, 0, len(req.Services))
	for _, s := range req.Services {
		names = append(names, s.Name)
	}
	existing, err := servicesNamed(ctx, cli, names)
	if err != nil {
		return agents.SwarmAdoptResp{}, err
	}
	for _, want := range req.Services {
		cur, ok := existing[want.Name]
		switch {
		case !ok || cur.ID != want.ID:
			return agents.SwarmAdoptResp{}, fmt.Errorf("%w: service %s is gone or was replaced; preview again", agents.ErrSwarmWriteRefused, want.Name)
		case cur.Spec.Labels[agents.LabelStackNamespace] != req.Stack:
			return agents.SwarmAdoptResp{}, fmt.Errorf("%w: service %s is not part of stack %s", agents.ErrSwarmWriteRefused, want.Name, req.Stack)
		case cur.Version.Index != want.Version && cur.Spec.Labels[agents.LabelSpecHash] == "":
			return agents.SwarmAdoptResp{}, fmt.Errorf("%w: service %s changed since the preview; preview again", agents.ErrSwarmWriteRefused, want.Name)
		}
	}
	out := agents.SwarmAdoptResp{Services: []agents.SwarmWriteResult{}}
	for _, want := range req.Services {
		cur := existing[want.Name]
		if cur.Spec.Labels[agents.LabelSpecHash] != "" {
			out.Services = append(out.Services, agents.SwarmWriteResult{Name: want.Name, ID: cur.ID, Action: agents.SwarmActionExists})
			continue
		}
		spec := cur.Spec
		spec.Labels = maps.Clone(spec.Labels)
		spec.Labels[agents.LabelSpecHash] = want.SpecHash
		res, err := cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{Version: cur.Version, Spec: spec})
		if err != nil {
			out.Services = append(out.Services, failed(ctx, want.Name, cur.ID, err))
			return out, nil
		}
		out.Services = append(out.Services, agents.SwarmWriteResult{Name: want.Name, ID: cur.ID, Action: agents.SwarmActionUpdated, Warnings: res.Warnings})
	}
	return out, nil
}

// Package swarmwrite answers the Swarm write frames from a local daemon,
// for the agent and for the server's own host. It does what
// `docker stack deploy` and `docker stack rm` do for the objects a request
// names, and nothing else: every name is checked against the stack's
// namespace label before the first write.
package swarmwrite

import (
	"context"
	"errors"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Timeout is below agents.SwarmWriteWait, so the agent answers with what
// it did instead of the server giving up first.
const Timeout = agents.SwarmWriteWait - time.Minute

// API is the part of the Docker client the write frames use.
type API interface {
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
	ServiceList(ctx context.Context, options client.ServiceListOptions) (client.ServiceListResult, error)
	ServiceCreate(ctx context.Context, options client.ServiceCreateOptions) (client.ServiceCreateResult, error)
	ServiceUpdate(ctx context.Context, serviceID string, options client.ServiceUpdateOptions) (client.ServiceUpdateResult, error)
	ServiceRemove(ctx context.Context, serviceID string, options client.ServiceRemoveOptions) (client.ServiceRemoveResult, error)
	NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
	NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
	SecretList(ctx context.Context, options client.SecretListOptions) (client.SecretListResult, error)
	ConfigList(ctx context.Context, options client.ConfigListOptions) (client.ConfigListResult, error)
}

// networkRemoveTries covers tasks that still hold the network for a few
// seconds after their service is removed.
var (
	networkRemoveTries = 5
	networkRemoveWait  = 2 * time.Second
)

func requireManager(ctx context.Context, cli API) error {
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("read swarm state: %w", err)
	}
	sw := res.Info.Swarm
	if sw.LocalNodeState != swarm.LocalNodeStateActive || !sw.ControlAvailable {
		return agents.ErrNotSwarmManager
	}
	return nil
}

// servicesNamed returns the services whose name is exactly one of names.
// The name filter matches prefixes, hence the map.
func servicesNamed(ctx context.Context, cli API, names []string) (map[string]swarm.Service, error) {
	out := map[string]swarm.Service{}
	if len(names) == 0 {
		return out, nil
	}
	f := client.Filters{}
	want := map[string]bool{}
	for _, n := range names {
		f = f.Add("name", n)
		want[n] = true
	}
	res, err := cli.ServiceList(ctx, client.ServiceListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	for _, s := range res.Items {
		if want[s.Spec.Name] {
			out[s.Spec.Name] = s
		}
	}
	return out, nil
}

// networksNamed returns the Swarm-scoped networks whose name is exactly one
// of names, and the names also taken by a network of another scope on this
// node. Two Swarm networks with one name are an error: which one a service
// would get is not defined.
func networksNamed(ctx context.Context, cli API, names []string) (map[string]network.Summary, map[string]bool, error) {
	out, local := map[string]network.Summary{}, map[string]bool{}
	if len(names) == 0 {
		return out, local, nil
	}
	f := client.Filters{}
	want := map[string]bool{}
	for _, n := range names {
		f = f.Add("name", n)
		want[n] = true
	}
	res, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return nil, nil, fmt.Errorf("list networks: %w", err)
	}
	for _, n := range res.Items {
		if !want[n.Name] {
			continue
		}
		if n.Scope != "swarm" {
			local[n.Name] = true
			continue
		}
		if _, dup := out[n.Name]; dup {
			return nil, nil, fmt.Errorf("%w: more than one network is named %s", agents.ErrSwarmWriteRefused, n.Name)
		}
		out[n.Name] = n
	}
	return out, local, nil
}

// ownedNetwork and ownedService report whether Dockmesh created the object
// for stack.
func ownedNetwork(n network.Summary, stack string) bool {
	return n.Labels[agents.LabelStackNamespace] == stack && n.Labels[agents.LabelDockmeshStack] == stack
}

func ownedService(s swarm.Service, stack string) bool {
	return s.Spec.Labels[agents.LabelStackNamespace] == stack && s.Spec.Labels[agents.LabelSpecHash] != ""
}

// resolveRefs fills in the ids of the secrets and configs the services
// reference by name. Names the Swarm doesn't know refuse the deploy.
func resolveRefs(ctx context.Context, cli API, svcs []agents.SwarmServiceWrite) error {
	sf, cf := client.Filters{}, client.Filters{}
	var anyS, anyC bool
	for _, w := range svcs {
		for _, r := range w.Spec.TaskTemplate.ContainerSpec.Secrets {
			sf, anyS = sf.Add("name", r.SecretName), true
		}
		for _, r := range w.Spec.TaskTemplate.ContainerSpec.Configs {
			cf, anyC = cf.Add("name", r.ConfigName), true
		}
	}
	secrets, configs := map[string]string{}, map[string]string{}
	if anyS {
		res, err := cli.SecretList(ctx, client.SecretListOptions{Filters: sf})
		if err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		for _, x := range res.Items {
			secrets[x.Spec.Name] = x.ID
		}
	}
	if anyC {
		res, err := cli.ConfigList(ctx, client.ConfigListOptions{Filters: cf})
		if err != nil {
			return fmt.Errorf("list configs: %w", err)
		}
		for _, x := range res.Items {
			configs[x.Spec.Name] = x.ID
		}
	}
	for _, w := range svcs {
		for _, r := range w.Spec.TaskTemplate.ContainerSpec.Secrets {
			if r.SecretID = secrets[r.SecretName]; r.SecretID == "" {
				return fmt.Errorf("%w: external secret %s not found", agents.ErrSwarmWriteRefused, r.SecretName)
			}
		}
		for _, r := range w.Spec.TaskTemplate.ContainerSpec.Configs {
			if r.ConfigID = configs[r.ConfigName]; r.ConfigID == "" {
				return fmt.Errorf("%w: external config %s not found", agents.ErrSwarmWriteRefused, r.ConfigName)
			}
		}
	}
	return nil
}

// Deploy applies req. Refusals and failed checks return an error before
// anything is written. Once writing started, a failed write stops the
// deploy and is reported in the answer's Error with every write done
// before it.
func Deploy(ctx context.Context, cli API, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := req.Validate(); err != nil {
		return agents.SwarmDeployResp{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmDeployResp{}, err
	}

	netNames := make([]string, 0, len(req.Networks)+len(req.ExternalNetworks))
	for _, n := range req.Networks {
		netNames = append(netNames, n.Name)
	}
	netNames = append(netNames, req.ExternalNetworks...)
	nets, local, err := networksNamed(ctx, cli, netNames)
	if err != nil {
		return agents.SwarmDeployResp{}, err
	}
	for _, name := range req.ExternalNetworks {
		if _, ok := nets[name]; ok {
			continue
		}
		if local[name] {
			return agents.SwarmDeployResp{}, fmt.Errorf("%w: external network %s is not scoped to the Swarm", agents.ErrSwarmWriteRefused, name)
		}
		return agents.SwarmDeployResp{}, fmt.Errorf("%w: external network %s not found", agents.ErrSwarmWriteRefused, name)
	}
	for _, want := range req.Networks {
		if local[want.Name] {
			return agents.SwarmDeployResp{}, fmt.Errorf("%w: a network named %s that is not scoped to the Swarm exists on this manager", agents.ErrSwarmWriteRefused, want.Name)
		}
		n, ok := nets[want.Name]
		if !ok {
			continue
		}
		if !ownedNetwork(n, req.Stack) {
			return agents.SwarmDeployResp{}, fmt.Errorf("%w: network %s exists and was not created by Dockmesh for stack %s", agents.ErrSwarmWriteRefused, want.Name, req.Stack)
		}
		if err := lessIsolated(n, want); err != nil {
			return agents.SwarmDeployResp{}, err
		}
	}

	svcNames := make([]string, 0, len(req.Services))
	for _, s := range req.Services {
		svcNames = append(svcNames, s.Spec.Name)
	}
	existing, err := servicesNamed(ctx, cli, svcNames)
	if err != nil {
		return agents.SwarmDeployResp{}, err
	}
	for name, s := range existing {
		if !ownedService(s, req.Stack) {
			return agents.SwarmDeployResp{}, fmt.Errorf("%w: service %s exists and was not deployed by Dockmesh for stack %s", agents.ErrSwarmWriteRefused, name, req.Stack)
		}
	}
	if err := resolveRefs(ctx, cli, req.Services); err != nil {
		return agents.SwarmDeployResp{}, err
	}

	out := agents.SwarmDeployResp{Networks: []agents.SwarmWriteResult{}, Services: []agents.SwarmWriteResult{}}
	for _, want := range req.Networks {
		if n, ok := nets[want.Name]; ok {
			out.Networks = append(out.Networks, agents.SwarmWriteResult{
				Name: want.Name, ID: n.ID, Action: agents.SwarmActionExists, Warnings: networkDrift(n, want),
			})
			continue
		}
		res, err := cli.NetworkCreate(ctx, want.Name, createOptions(want))
		if err != nil {
			r := failed(ctx, want.Name, "", err)
			out.Networks = append(out.Networks, r)
			out.Error = fmt.Sprintf("create network %s: %s", want.Name, r.Error)
			return out, nil
		}
		out.Networks = append(out.Networks, agents.SwarmWriteResult{Name: want.Name, ID: res.ID, Action: agents.SwarmActionCreated, Warnings: res.Warning})
	}

	for _, w := range req.Services {
		r := writeService(ctx, cli, w, existing)
		out.Services = append(out.Services, r)
		if r.Action == agents.SwarmActionFailed || r.Action == agents.SwarmActionUnknown {
			out.Error = fmt.Sprintf("%s: %s", w.Spec.Name, r.Error)
			return out, nil
		}
	}
	return out, nil
}

func writeService(ctx context.Context, cli API, w agents.SwarmServiceWrite, existing map[string]swarm.Service) agents.SwarmWriteResult {
	spec := w.Spec
	name := spec.Name
	cur, ok := existing[name]
	if !ok {
		res, err := cli.ServiceCreate(ctx, client.ServiceCreateOptions{
			Spec: spec, EncodedRegistryAuth: w.RegistryAuth, QueryRegistry: true,
		})
		if err != nil {
			return failed(ctx, name, "", err)
		}
		return agents.SwarmWriteResult{Name: name, ID: res.ID, Action: agents.SwarmActionCreated, Warnings: res.Warnings}
	}
	// As the CLI: a file without replicas keeps the running count, and a
	// deploy never resets a force-update counter.
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas == nil && cur.Spec.Mode.Replicated != nil {
		spec.Mode.Replicated.Replicas = cur.Spec.Mode.Replicated.Replicas
	}
	spec.TaskTemplate.ForceUpdate = cur.Spec.TaskTemplate.ForceUpdate
	res, err := cli.ServiceUpdate(ctx, cur.ID, client.ServiceUpdateOptions{
		Version: cur.Version, Spec: spec, EncodedRegistryAuth: w.RegistryAuth, QueryRegistry: true,
	})
	if err != nil {
		return failed(ctx, name, cur.ID, err)
	}
	return agents.SwarmWriteResult{Name: name, ID: cur.ID, Action: agents.SwarmActionUpdated, Warnings: res.Warnings}
}

// failed is the result of a write that returned err. A write cut off by
// the deadline or a cancel may still have been applied by the daemon.
func failed(ctx context.Context, name, id string, err error) agents.SwarmWriteResult {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return agents.SwarmWriteResult{Name: name, ID: id, Action: agents.SwarmActionUnknown, Error: "cut off before the daemon answered; it may have been applied"}
	}
	return agents.SwarmWriteResult{Name: name, ID: id, Action: agents.SwarmActionFailed, Error: err.Error()}
}

func createOptions(r network.CreateRequest) client.NetworkCreateOptions {
	return client.NetworkCreateOptions{
		Driver:     r.Driver,
		Scope:      "swarm",
		EnableIPv4: r.EnableIPv4,
		EnableIPv6: r.EnableIPv6,
		IPAM:       r.IPAM,
		Internal:   r.Internal,
		Attachable: r.Attachable,
		Options:    r.Options,
		Labels:     r.Labels,
	}
}

// lessIsolated refuses an existing network that is open where the file
// asks for an internal or encrypted one: the services would join it as it
// is.
func lessIsolated(have network.Summary, want network.CreateRequest) error {
	if want.Internal && !have.Internal {
		return fmt.Errorf("%w: network %s exists and is not internal; remove it to create it as the file asks", agents.ErrSwarmWriteRefused, want.Name)
	}
	if _, enc := want.Options["encrypted"]; enc {
		if _, ok := have.Options["encrypted"]; !ok {
			return fmt.Errorf("%w: network %s exists and is not encrypted; remove it to create it as the file asks", agents.ErrSwarmWriteRefused, want.Name)
		}
	}
	return nil
}

// networkDrift names what an existing network has other than the file
// asks for. Like the CLI the deploy keeps the network as it is; a change
// only takes effect once the network is removed and created again.
func networkDrift(have network.Summary, want network.CreateRequest) []string {
	var out []string
	if have.Driver != want.Driver {
		out = append(out, fmt.Sprintf("network %s uses driver %s, the file asks for %s", want.Name, have.Driver, want.Driver))
	}
	if have.Internal != want.Internal {
		out = append(out, fmt.Sprintf("network %s: internal is %t, the file asks for %t", want.Name, have.Internal, want.Internal))
	}
	if have.Attachable != want.Attachable {
		out = append(out, fmt.Sprintf("network %s: attachable is %t, the file asks for %t", want.Name, have.Attachable, want.Attachable))
	}
	if want.IPAM != nil && len(want.IPAM.Config) > 0 && !sameSubnets(have.IPAM, *want.IPAM) {
		out = append(out, fmt.Sprintf("network %s has other subnets than the file asks for", want.Name))
	}
	return out
}

func sameSubnets(a, b network.IPAM) bool {
	if len(a.Config) != len(b.Config) {
		return false
	}
	for i := range a.Config {
		if a.Config[i].Subnet != b.Config[i].Subnet {
			return false
		}
	}
	return true
}

// Remove removes the named services, then the named networks, of one
// stack. A name carrying another stack's label, or none, refuses the
// whole request before the first removal.
func Remove(ctx context.Context, cli API, req agents.SwarmRemoveReq) (agents.SwarmRemoveResp, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := req.Validate(); err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	svcs, err := servicesNamed(ctx, cli, req.Services)
	if err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	for name, s := range svcs {
		if !ownedService(s, req.Stack) {
			return agents.SwarmRemoveResp{}, fmt.Errorf("%w: service %s was not deployed by Dockmesh for stack %s", agents.ErrSwarmWriteRefused, name, req.Stack)
		}
	}
	nets, _, err := networksNamed(ctx, cli, req.Networks)
	if err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	for name, n := range nets {
		if !ownedNetwork(n, req.Stack) {
			return agents.SwarmRemoveResp{}, fmt.Errorf("%w: network %s was not created by Dockmesh for stack %s", agents.ErrSwarmWriteRefused, name, req.Stack)
		}
	}

	out := agents.SwarmRemoveResp{Services: []agents.SwarmWriteResult{}, Networks: []agents.SwarmWriteResult{}}
	for _, name := range req.Services {
		s, ok := svcs[name]
		if !ok {
			out.Services = append(out.Services, agents.SwarmWriteResult{Name: name, Action: agents.SwarmActionMissing})
			continue
		}
		r := agents.SwarmWriteResult{Name: name, ID: s.ID, Action: agents.SwarmActionRemoved}
		if _, err := cli.ServiceRemove(ctx, s.ID, client.ServiceRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			r = failed(ctx, name, s.ID, err)
		}
		out.Services = append(out.Services, r)
	}
	for _, name := range req.Networks {
		n, ok := nets[name]
		if !ok {
			out.Networks = append(out.Networks, agents.SwarmWriteResult{Name: name, Action: agents.SwarmActionMissing})
			continue
		}
		r := agents.SwarmWriteResult{Name: name, ID: n.ID, Action: agents.SwarmActionRemoved}
		if err := removeNetwork(ctx, cli, n.ID); err != nil {
			r = failed(ctx, name, n.ID, err)
		}
		out.Networks = append(out.Networks, r)
	}
	return out, nil
}

func removeNetwork(ctx context.Context, cli API, id string) error {
	var err error
	for i := 0; i < networkRemoveTries; i++ {
		_, err = cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
		if err == nil || cerrdefs.IsNotFound(err) {
			return nil
		}
		if i+1 < networkRemoveTries {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(networkRemoveWait):
			}
		}
	}
	return err
}

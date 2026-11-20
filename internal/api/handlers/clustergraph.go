package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/topology"
)

// clusterTopology answers GET /networks/topology?cluster=<id>: the Swarm's
// services and the Swarm-scoped networks they attach to, read through the
// cluster's manager. Services are named, so it needs stacks.view on the
// cluster next to the route's networks.view.
func (h *Handlers) clusterTopology(w http.ResponseWriter, r *http.Request, id string) {
	res := rbac.Resource{ClusterID: id}
	if !h.allowedOn(r, res, rbac.PermStacksView) {
		h.writeScopeDenied(w, r, h.missingOn(r, res, rbac.PermStacksView), res, "cluster "+id)
		return
	}
	hostID, ok := h.clusterReadHostOf(w, r, id)
	if !ok {
		return
	}
	nets, err := h.Hosts.SwarmNetworks(r.Context(), hostID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	svcs, err := h.Hosts.SwarmServices(r.Context(), hostID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	name := id
	if c, err := h.Clusters.Get(r.Context(), id); err == nil {
		name = c.Name
	}
	writeJSON(w, http.StatusOK, Topology{
		Target:    TopoTarget{Kind: "cluster", ID: id, Name: name},
		Graph:     clusterGraph(nets.Networks, svcs.Services),
		Services:  svcs.Services,
		Truncated: nets.Truncated || svcs.Truncated,
	})
}

// missingOn names the first of the route's permissions and extra that the
// caller lacks on res, for the denial message and audit entry.
func (h *Handlers) missingOn(r *http.Request, res rbac.Resource, extra ...rbac.Perm) rbac.Perm {
	acc := middleware.AccessOf(r.Context())
	perms := scopePerms(r, extra...)
	for _, p := range perms {
		if !acc.AllowsOn(p, res) {
			return p
		}
	}
	if len(perms) == 0 {
		return routePerm(r)
	}
	return perms[len(perms)-1]
}

// clusterGraph links every service to the networks it attaches to. A
// service spec may name a network instead of giving its id; references to
// networks the manager didn't list are dropped. Ingress counts as a system
// network, hidden by default like bridge and host.
func clusterGraph(nets []agents.SwarmNetwork, svcs []agents.SwarmService) topology.Graph {
	g := topology.Graph{Networks: []topology.Network{}, Containers: []topology.Container{}, Links: []topology.Link{}}
	ids := make(map[string]string, 2*len(nets))
	for _, n := range nets {
		g.Networks = append(g.Networks, topology.Network{
			ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: "swarm",
			Internal: n.Internal, System: n.Ingress, Stack: n.Stack,
		})
		ids[n.ID] = n.ID
		if _, taken := ids[n.Name]; !taken {
			ids[n.Name] = n.ID
		}
	}
	for _, s := range svcs {
		seen := map[string]bool{}
		for _, ref := range s.Networks {
			nid, ok := ids[ref]
			if !ok || seen[nid] {
				continue
			}
			seen[nid] = true
			g.Links = append(g.Links, topology.Link{NetworkID: nid, ServiceID: s.ID})
		}
	}
	sort.Slice(g.Links, func(i, j int) bool {
		a, b := g.Links[i], g.Links[j]
		if a.NetworkID != b.NetworkID {
			return a.NetworkID < b.NetworkID
		}
		return a.ServiceID < b.ServiceID
	})
	return g
}

// clusterNetwork is a Swarm-scoped network with the services attached to
// it; Services is left out for callers without stacks.view on the cluster.
type clusterNetwork struct {
	agents.SwarmNetwork
	Services []serviceRef `json:"services,omitempty"`
}

type serviceRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stack string `json:"stack,omitempty"`
}

// attachServices adds to every network the services the graph links to it.
func attachServices(nets []agents.SwarmNetwork, svcs []agents.SwarmService) []clusterNetwork {
	g := clusterGraph(nets, svcs)
	byID := make(map[string]agents.SwarmService, len(svcs))
	for _, s := range svcs {
		byID[s.ID] = s
	}
	attached := map[string][]serviceRef{}
	for _, l := range g.Links {
		s := byID[l.ServiceID]
		attached[l.NetworkID] = append(attached[l.NetworkID], serviceRef{ID: s.ID, Name: s.Name, Stack: s.Stack})
	}
	out := make([]clusterNetwork, 0, len(nets))
	for _, n := range nets {
		refs := attached[n.ID]
		sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
		out = append(out, clusterNetwork{SwarmNetwork: n, Services: refs})
	}
	return out
}

// clusterStackVolume is one volume of a Swarm stack and the nodes with an
// agent that hold a copy of it.
type clusterStackVolume struct {
	Stack string              `json:"stack"`
	Name  string              `json:"name"`
	Nodes []clusterVolumeNode `json:"nodes"`
}

type clusterVolumeNode struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Driver   string `json:"driver"`
}

type clusterVolumesResponse struct {
	Volumes []clusterStackVolume `json:"volumes"`
	// ReadHosts are the member hosts that answered; nodes without an agent
	// are never among them, so their volumes are not listed.
	ReadHosts   []string           `json:"read_hosts"`
	Unreachable []host.Unreachable `json:"unreachable_hosts"`
}

// ClusterVolumes lists the volumes of Swarm stacks per stack, with the
// member hosts (nodes with an agent) each one exists on. The hosts are
// the cluster's node inventory, so it needs hosts.view on the cluster
// next to the route's volumes.view.
//
//	GET /api/v1/clusters/{id}/volumes
func (h *Handlers) ClusterVolumes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if res := (rbac.Resource{ClusterID: id}); !h.allowedOn(r, res, rbac.PermHostsView) {
		h.writeScopeDenied(w, r, h.missingOn(r, res, rbac.PermHostsView), res, "cluster "+id)
		return
	}
	if h.Clusters == nil || h.Hosts == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	c, err := h.Clusters.Get(r.Context(), id)
	if errors.Is(err, clusters.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := clusterVolumesResponse{Volumes: []clusterStackVolume{}, ReadHosts: []string{}, Unreachable: []host.Unreachable{}}
	names := map[string]string{}
	if infos, err := h.Hosts.List(r.Context()); err == nil {
		for _, i := range infos {
			names[i.ID] = i.Name
		}
	}
	var targets []host.Host
	for _, m := range c.Members {
		hh, err := h.Hosts.Pick(m.HostID)
		if err != nil {
			name := names[m.HostID]
			if name == "" {
				name = m.HostID
			}
			out.Unreachable = append(out.Unreachable, host.Unreachable{HostID: m.HostID, HostName: name, Reason: err.Error()})
			continue
		}
		targets = append(targets, hh)
	}
	type stackVolume struct {
		node        clusterVolumeNode
		stack, name string
	}
	type hostVolumes struct {
		hostID string
		vols   []stackVolume
	}
	fan := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]hostVolumes, error) {
		list, err := hh.ListVolumes(ctx)
		if err != nil {
			return nil, err
		}
		hv := hostVolumes{hostID: hh.ID()}
		for _, v := range list {
			m := toMap(v)
			labels, _ := m["Labels"].(map[string]any)
			stack, _ := labels[agents.LabelStackNamespace].(string)
			name, _ := m["Name"].(string)
			// A compose project that sets the label itself is a host stack.
			if _, compose := labels["com.docker.compose.project"]; compose || stack == "" || name == "" {
				continue
			}
			driver, _ := m["Driver"].(string)
			hv.vols = append(hv.vols, stackVolume{node: clusterVolumeNode{HostID: hh.ID(), HostName: hh.Name(), Driver: driver}, stack: stack, name: name})
		}
		return []hostVolumes{hv}, nil
	})
	out.Unreachable = append(out.Unreachable, fan.Unreachable...)
	byKey := map[[2]string]*clusterStackVolume{}
	for _, hv := range fan.Items {
		out.ReadHosts = append(out.ReadHosts, hv.hostID)
		for _, sv := range hv.vols {
			k := [2]string{sv.stack, sv.name}
			v := byKey[k]
			if v == nil {
				v = &clusterStackVolume{Stack: sv.stack, Name: sv.name}
				byKey[k] = v
			}
			v.Nodes = append(v.Nodes, sv.node)
		}
	}
	for _, v := range byKey {
		sort.Slice(v.Nodes, func(i, j int) bool { return v.Nodes[i].HostName < v.Nodes[j].HostName })
		out.Volumes = append(out.Volumes, *v)
	}
	sort.Slice(out.Volumes, func(i, j int) bool {
		a, b := out.Volumes[i], out.Volumes[j]
		if a.Stack != b.Stack {
			return a.Stack < b.Stack
		}
		return a.Name < b.Name
	})
	sort.Strings(out.ReadHosts)
	writeJSON(w, http.StatusOK, out)
}

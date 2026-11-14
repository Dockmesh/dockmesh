package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/topology"
)

// Topology is the network graph of one target.
type Topology struct {
	Target TopoTarget `json:"target"`
	topology.Graph
}

type TopoTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GetTopology returns the network graph of the host named by ?host=
// (the server's own when empty), cut to what the caller may see.
func (h *Handlers) GetTopology(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Has("cluster") {
		writeError(w, http.StatusBadRequest, "cluster topology is not supported")
		return
	}
	if host.IsAll(q.Get("host")) {
		writeError(w, http.StatusBadRequest, "topology needs one host, not all hosts")
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	g, err := target.Topology(r.Context())
	if errors.Is(err, host.ErrAgentNoTopology) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error(), "code": "topology_agent_outdated"})
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, host.ErrAgentOffline) || errors.Is(err, host.ErrNoDocker) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Topology{
		Target: TopoTarget{Kind: "host", ID: target.ID(), Name: target.Name()},
		Graph:  h.scopeTopology(r, target.ID(), g),
	})
}

// scopeTopology keeps the containers of stacks in the caller's scope and
// the networks it sees, plus any shared network one of those containers
// sits on. Host-level containers and networks need the host in scope.
func (h *Handlers) scopeTopology(r *http.Request, hostID string, g topology.Graph) topology.Graph {
	if h.callerUnscoped(r) {
		return g
	}
	out := topology.Graph{Networks: []topology.Network{}, Containers: []topology.Container{}, Links: []topology.Link{}}
	containers := map[string]bool{}
	for _, c := range g.Containers {
		if h.inReadScope(r, hostID, c.Stack) {
			out.Containers = append(out.Containers, c)
			containers[c.ID] = true
		}
	}
	reached := map[string]bool{}
	for _, l := range g.Links {
		if containers[l.ContainerID] {
			reached[l.NetworkID] = true
		}
	}
	networks := map[string]bool{}
	for _, n := range g.Networks {
		if reached[n.ID] || h.inReadScope(r, hostID, n.Stack) {
			out.Networks = append(out.Networks, n)
			networks[n.ID] = true
		}
	}
	for _, l := range g.Links {
		if containers[l.ContainerID] && networks[l.NetworkID] {
			out.Links = append(out.Links, l)
		}
	}
	return out
}

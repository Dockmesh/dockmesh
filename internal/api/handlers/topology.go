package handlers

import (
	"net/http"
	"strings"

	dtypes "github.com/docker/docker/api/types"
)

// Topology is the network-graph payload returned to the frontend.
// It's small enough to send as a single response (each container/network is
// only its identity + a handful of summary fields). The frontend lays out
// the graph client-side.
type Topology struct {
	Networks   []TopoNetwork   `json:"networks"`
	Containers []TopoContainer `json:"containers"`
	Links      []TopoLink      `json:"links"`
}

type TopoNetwork struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Scope    string `json:"scope"`
	Internal bool   `json:"internal"`
	System   bool   `json:"system"` // bridge/host/none — usually filtered in the UI
	Stack    string `json:"stack,omitempty"`
}

type TopoContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Image string `json:"image"`
	Stack string `json:"stack,omitempty"`
}

type TopoLink struct {
	NetworkID   string   `json:"network_id"`
	ContainerID string   `json:"container_id"`
	IPv4        string   `json:"ipv4,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// GetTopology builds the topology by listing networks (verbose inspect to
// get container endpoints) and containers in parallel. Containers without
// any networks (e.g. host-mode) are still included so they appear in the UI.
func (h *Handlers) GetTopology(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	ctx := r.Context()

	netList, err := h.Docker.ListNetworks(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	containers, err := h.Docker.ListContainers(ctx, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	topo := Topology{
		Networks:   []TopoNetwork{},
		Containers: []TopoContainer{},
		Links:      []TopoLink{},
	}

	containerByID := make(map[string]bool, len(containers))
	for _, c := range containers {
		name := strings.TrimPrefix(firstName(c.Names), "/")
		stack := ""
		if c.Labels != nil {
			stack = c.Labels["com.docker.compose.project"]
		}
		topo.Containers = append(topo.Containers, TopoContainer{
			ID:    c.ID,
			Name:  name,
			State: c.State,
			Image: c.Image,
			Stack: stack,
		})
		containerByID[c.ID] = true
	}

	for _, n := range netList {
		// Verbose inspect gives us the Containers map. ListNetworks alone
		// returns it empty.
		full, err := h.Docker.InspectNetwork(ctx, n.ID)
		if err != nil {
			continue
		}
		stack := ""
		if full.Labels != nil {
			stack = full.Labels["com.docker.compose.project"]
		}
		topo.Networks = append(topo.Networks, TopoNetwork{
			ID:       full.ID,
			Name:     full.Name,
			Driver:   full.Driver,
			Scope:    full.Scope,
			Internal: full.Internal,
			System:   full.Name == "bridge" || full.Name == "host" || full.Name == "none",
			Stack:    stack,
		})
		for cid, ep := range full.Containers {
			if !containerByID[cid] {
				continue
			}
			topo.Links = append(topo.Links, TopoLink{
				NetworkID:   full.ID,
				ContainerID: cid,
				IPv4:        stripCIDR(ep.IPv4Address),
			})
		}
	}

	writeJSON(w, http.StatusOK, topo)
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func stripCIDR(s string) string {
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i]
	}
	return s
}

// silence unused import if dtypes is otherwise unused in the file
var _ = dtypes.Container{}

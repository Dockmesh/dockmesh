// Package topology builds the network graph of one Docker host: its
// networks, its containers and which container sits on which network.
// The server builds it for its own host and agents build it for theirs,
// so both answer with the same shape.
package topology

import (
	"fmt"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

const stackLabel = "com.docker.compose.project"

// Graph is unfiltered: the server applies the caller's scope before it
// answers an API request.
type Graph struct {
	Networks   []Network   `json:"networks"`
	Containers []Container `json:"containers"`
	Links      []Link      `json:"links"`
}

type Network struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Scope    string `json:"scope"`
	Internal bool   `json:"internal"`
	System   bool   `json:"system"` // bridge/host/none, hidden by default in the UI
	Stack    string `json:"stack,omitempty"`
}

type Container struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Image string `json:"image"`
	Stack string `json:"stack,omitempty"`
	Ports []Port `json:"ports,omitempty"`
}

type Port struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

type Link struct {
	NetworkID   string   `json:"network_id"`
	ContainerID string   `json:"container_id"`
	IPv4        string   `json:"ipv4,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// Build derives the graph from a network list and a container list
// (stopped containers included). Memberships come from each container's
// NetworkSettings, so no per-network inspect is needed.
func Build(nets []network.Inspect, containers []container.Summary) Graph {
	g := Graph{Networks: []Network{}, Containers: []Container{}, Links: []Link{}}

	known := make(map[string]bool, len(nets))
	for _, n := range nets {
		known[n.ID] = true
		g.Networks = append(g.Networks, Network{
			ID:       n.ID,
			Name:     n.Name,
			Driver:   n.Driver,
			Scope:    n.Scope,
			Internal: n.Internal,
			System:   n.Name == "bridge" || n.Name == "host" || n.Name == "none",
			Stack:    n.Labels[stackLabel],
		})
	}

	for _, c := range containers {
		g.Containers = append(g.Containers, Container{
			ID:    c.ID,
			Name:  strings.TrimPrefix(firstName(c.Names), "/"),
			State: string(c.State),
			Image: c.Image,
			Stack: c.Labels[stackLabel],
			Ports: publishedPorts(c.Ports),
		})
		if c.NetworkSettings == nil {
			continue
		}
		for _, ep := range c.NetworkSettings.Networks {
			if ep == nil || !known[ep.NetworkID] {
				continue
			}
			l := Link{NetworkID: ep.NetworkID, ContainerID: c.ID}
			if ep.IPAddress.IsValid() {
				l.IPv4 = ep.IPAddress.String()
			}
			g.Links = append(g.Links, l)
		}
	}
	// Map iteration above has no order; a stable answer keeps the UI
	// from reshuffling on every reload.
	sort.Slice(g.Links, func(i, j int) bool {
		a, b := g.Links[i], g.Links[j]
		if a.NetworkID != b.NetworkID {
			return a.NetworkID < b.NetworkID
		}
		return a.ContainerID < b.ContainerID
	})
	return g
}

// publishedPorts drops unpublished ports and the second entry Docker
// reports per port when it binds both 0.0.0.0 and ::.
func publishedPorts(in []container.PortSummary) []Port {
	seen := make(map[string]bool)
	var out []Port
	for _, p := range in {
		if p.PublicPort == 0 {
			continue
		}
		key := fmt.Sprintf("%d/%s", p.PublicPort, p.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Port{HostPort: int(p.PublicPort), ContainerPort: int(p.PrivatePort), Protocol: p.Type})
	}
	return out
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

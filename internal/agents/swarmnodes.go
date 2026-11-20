package agents

import (
	"strings"

	"github.com/moby/moby/api/types/swarm"
)

// SwarmNode is one entry of a Swarm manager's node list. Only a manager
// can list nodes; the list is what links hosts to a cluster.
type SwarmNode struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname,omitempty"`
	Role         string `json:"role"` // "manager" | "worker"
	State        string `json:"state,omitempty"`
	Availability string `json:"availability,omitempty"`
	Leader       bool   `json:"leader,omitempty"`
	Reachability string `json:"reachability,omitempty"`
}

// SwarmNodesResp is the agent's answer to FrameReqSwarmNodes.
type SwarmNodesResp struct {
	Nodes []SwarmNode `json:"nodes"`
}

// maxSwarmNodes bounds what the server accepts from one manager.
const maxSwarmNodes = 2000

// SwarmNodesFrom converts a daemon's node list.
func SwarmNodesFrom(nodes []swarm.Node) []SwarmNode {
	out := make([]SwarmNode, 0, len(nodes))
	for _, n := range nodes {
		sn := SwarmNode{
			ID:           n.ID,
			Hostname:     n.Description.Hostname,
			Role:         string(n.Spec.Role),
			State:        string(n.Status.State),
			Availability: string(n.Spec.Availability),
		}
		if n.ManagerStatus != nil {
			sn.Leader = n.ManagerStatus.Leader
			sn.Reachability = string(n.ManagerStatus.Reachability)
		}
		out = append(out, sn)
	}
	return SanitizeSwarmNodes(out)
}

// SanitizeSwarmNodes drops entries whose id or role don't look like what
// dockerd reports and blanks fields that don't fit their format. The list
// comes from the agent host and is not trusted beyond that.
func SanitizeSwarmNodes(nodes []SwarmNode) []SwarmNode {
	out := make([]SwarmNode, 0, min(len(nodes), maxSwarmNodes))
	seen := map[string]bool{}
	for _, n := range nodes {
		if len(out) == maxSwarmNodes {
			break
		}
		id := token(n.ID, 64, "")
		if id == "" || seen[id] || (n.Role != "manager" && n.Role != "worker") {
			continue
		}
		seen[id] = true
		out = append(out, SwarmNode{
			ID:           id,
			Hostname:     hostname(n.Hostname),
			Role:         n.Role,
			State:        token(n.State, 16, ""),
			Availability: token(n.Availability, 16, ""),
			Leader:       n.Leader && n.Role == "manager",
			Reachability: token(n.Reachability, 16, ""),
		})
	}
	return out
}

// hostname returns s when it is a plausible host name (letters, digits,
// '.', '-', '_', at most 253 characters), else "".
func hostname(s string) string {
	if len(s) > 253 {
		return ""
	}
	if token(strings.ToLower(s), 253, ".-_") == "" {
		return ""
	}
	return s
}

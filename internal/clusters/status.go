package clusters

import (
	"context"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Cluster states. They come from the origin manager's view only.
const (
	StateHealthy     = "healthy"
	StateDegraded    = "degraded"
	StateNoQuorum    = "no_quorum"
	StateUnreachable = "unreachable"
	StateUnknown     = "unknown"
)

// Status is a cluster's state as its origin manager last reported it.
// The node counts come from the last node list the origin returned
// (KnownAt), so an unreachable cluster still shows its last known shape.
type Status struct {
	State             string     `json:"state"`
	Managers          int        `json:"managers"`
	ManagersReachable int        `json:"managers_reachable"`
	Workers           int        `json:"workers"`
	NodesDown         int        `json:"nodes_down"`
	NodesWithoutAgent int        `json:"nodes_without_agent"`
	Leader            string     `json:"leader,omitempty"`
	CheckedAt         *time.Time `json:"checked_at,omitempty"`
	KnownAt           *time.Time `json:"known_at,omitempty"`
}

// NodeStatus is one linked node's entry in the last node list.
type NodeStatus struct {
	State        string `json:"state,omitempty"`
	Availability string `json:"availability,omitempty"`
	Reachability string `json:"reachability,omitempty"`
	Leader       bool   `json:"leader,omitempty"`
}

// Membership is one host's link to a cluster, for the host's own pages.
type Membership struct {
	HostID      string      `json:"host_id"`
	ClusterID   string      `json:"cluster_id"`
	ClusterName string      `json:"cluster_name"`
	NodeID      string      `json:"node_id"`
	Role        string      `json:"role"`
	Node        *NodeStatus `json:"node,omitempty"`
}

// seen is what the last reconcile learned from a cluster's origin.
type seen struct {
	state     string
	checkedAt time.Time
	nodes     []agents.SwarmNode
	knownAt   time.Time
}

// record keeps the outcome of asking the origin. A failed ask keeps the
// last node list.
func (s *Service) record(clusterID, state string, nodes []agents.SwarmNode, at time.Time) {
	s.smu.Lock()
	defer s.smu.Unlock()
	if s.seen == nil {
		s.seen = map[string]seen{}
	}
	v := s.seen[clusterID]
	v.state, v.checkedAt = state, at
	if nodes != nil {
		v.nodes, v.knownAt = nodes, at
	}
	s.seen[clusterID] = v
}

func (s *Service) forget(clusterID string) {
	s.smu.Lock()
	delete(s.seen, clusterID)
	s.smu.Unlock()
}

func (s *Service) seenOf(clusterID string) (seen, bool) {
	s.smu.Lock()
	defer s.smu.Unlock()
	v, ok := s.seen[clusterID]
	return v, ok
}

// stateOf derives the state from a node list the origin returned: any
// manager that is not reachable or any node that is not ready makes the
// cluster degraded. Quorum held, since a manager without it can't list.
func stateOf(nodes []agents.SwarmNode) string {
	for _, n := range nodes {
		if n.State != "ready" {
			return StateDegraded
		}
		if n.Role == RoleManager && n.Reachability != "reachable" {
			return StateDegraded
		}
	}
	return StateHealthy
}

// lostQuorum reports whether a node list error is dockerd saying the
// Swarm has no leader.
func lostQuorum(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not have a leader")
}

// statusOf builds the status of c from what the last reconcile saw.
func (s *Service) statusOf(c Cluster) *Status {
	v, ok := s.seenOf(c.ID)
	if !ok {
		return &Status{State: StateUnknown}
	}
	st := &Status{State: v.state}
	checked := v.checkedAt
	st.CheckedAt = &checked
	if v.nodes == nil {
		return st
	}
	known := v.knownAt
	st.KnownAt = &known
	linked := map[string]bool{}
	for _, m := range c.Members {
		linked[m.NodeID] = true
	}
	for _, n := range v.nodes {
		switch n.Role {
		case RoleManager:
			st.Managers++
			if n.Reachability == "reachable" {
				st.ManagersReachable++
			}
			if n.Leader {
				st.Leader = n.Hostname
			}
		case RoleWorker:
			st.Workers++
		}
		if n.State != "ready" {
			st.NodesDown++
		}
		if !linked[n.ID] {
			st.NodesWithoutAgent++
		}
	}
	return st
}

// Memberships lists every linked host with its cluster and, when the last
// check returned a node list, its node's state.
func (s *Service) Memberships(ctx context.Context) ([]Membership, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []Membership{}
	for _, c := range list {
		// A list from an earlier check would show a leader that may be gone.
		v, _ := s.seenOf(c.ID)
		byID := map[string]agents.SwarmNode{}
		if v.state == StateHealthy || v.state == StateDegraded {
			for _, n := range v.nodes {
				byID[n.ID] = n
			}
		}
		for _, m := range c.Members {
			ms := Membership{HostID: m.HostID, ClusterID: c.ID, ClusterName: c.Name, NodeID: m.NodeID, Role: m.Role}
			if n, ok := byID[m.NodeID]; ok {
				ms.Node = &NodeStatus{State: n.State, Availability: n.Availability, Reachability: n.Reachability, Leader: n.Leader}
			}
			out = append(out, ms)
		}
	}
	return out, nil
}

// LastNodes returns the node list the last successful check of the
// cluster's origin returned and when, or ok false when there is none.
func (s *Service) LastNodes(clusterID string) (nodes []agents.SwarmNode, knownAt time.Time, ok bool) {
	v, found := s.seenOf(clusterID)
	if !found || v.nodes == nil {
		return nil, time.Time{}, false
	}
	return v.nodes, v.knownAt, true
}

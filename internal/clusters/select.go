package clusters

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// readers returns the hosts that may serve c's Swarm reads, in the order
// they are tried. Only managers that proved their node identity take part
// in failover: the current reader first, so a working choice sticks, then
// the leader, then the origin, then the others by host id. An unverified
// origin comes last, so an install whose agents can't prove yet keeps its
// read-only view. While a changed root CA waits, only the origin is used.
func (s *Service) readers(c Cluster, nodes []agents.SwarmNode) []string {
	if c.Trust.Pending {
		return []string{c.OriginHost}
	}
	byNode := map[string]agents.SwarmNode{}
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	var verified []string
	leader := ""
	for _, m := range c.Members {
		if m.Role != RoleManager || !m.Verified {
			continue
		}
		verified = append(verified, m.HostID)
		if byNode[m.NodeID].Leader {
			leader = m.HostID
		}
	}
	sort.Strings(verified)
	out := make([]string, 0, len(verified)+1)
	add := func(h string) {
		if h == "" {
			return
		}
		for _, x := range out {
			if x == h {
				return
			}
		}
		out = append(out, h)
	}
	has := func(h string) bool {
		for _, v := range verified {
			if v == h {
				return true
			}
		}
		return false
	}
	if cur := s.readerOf(c.ID); has(cur) {
		add(cur)
	}
	add(leader)
	if has(c.OriginHost) {
		add(c.OriginHost)
	}
	for _, h := range verified {
		add(h)
	}
	add(c.OriginHost)
	return out
}

func (s *Service) readerOf(id string) string {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return s.reader[id]
}

func (s *Service) setReader(id, host string) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.reader == nil {
		s.reader = map[string]string{}
	}
	if old := s.reader[id]; old != host {
		if old != "" {
			slog.Info("swarm reads switched to another manager", "cluster", id, "from", old, "to", host)
		}
		s.reader[id] = host
	}
}

// askManagers returns the node list of the first reader that answers as
// the linked manager node it is, the state that list shows and who gave
// it. With none answering the cluster is unreachable, or without quorum
// when a linked manager reported that.
func (s *Service) askManagers(ctx context.Context, c Cluster, snap map[string]hostState) (agents.SwarmNodesResp, string, string) {
	var nodes []agents.SwarmNode
	if v, ok := s.seenOf(c.ID); ok {
		nodes = v.nodes
	}
	state := StateUnreachable
	for _, h := range s.readers(c, nodes) {
		list, st := s.askManager(ctx, c, h, snap)
		if list.Nodes != nil {
			s.setReader(c.ID, h)
			return list, st, h
		}
		if st == StateNoQuorum {
			state = StateNoQuorum
		}
	}
	return agents.SwarmNodesResp{}, state, ""
}

// askManager asks one manager member. The list is nil when host is not the
// linked manager node of this Swarm right now. A linked manager whose
// dockerd no longer knows the cluster, or refuses the list for want of a
// leader, has lost quorum.
func (s *Service) askManager(ctx context.Context, c Cluster, host string, snap map[string]hostState) (agents.SwarmNodesResp, string) {
	var none agents.SwarmNodesResp
	linked := nodeOf(c, host)
	st := snap[host]
	sw := st.swarm
	if linked == "" || !st.known || st.offline || sw == nil || sw.NodeState != "active" || sw.Role != RoleManager || sw.NodeID != linked {
		return none, StateUnreachable
	}
	if sw.ClusterID == "" {
		return none, StateNoQuorum
	}
	if !isLinkedManager(c, host, &agents.DaemonInfo{Swarm: sw}) {
		return none, StateUnreachable
	}
	list, err := s.nodesOf(ctx, host)
	if err != nil {
		slog.Debug("swarm node list failed", "cluster", c.ID, "host", host, "err", err)
		if lostQuorum(err) {
			return none, StateNoQuorum
		}
		return none, StateUnreachable
	}
	if !listsManager(list.Nodes, linked) {
		return none, StateUnreachable
	}
	return list, stateOf(list.Nodes)
}

// ReadHost returns the host the cluster's Swarm state is read through: the
// first reader (see readers) that is, checked live, still the linked
// manager node of this Swarm.
func (s *Service) ReadHost(ctx context.Context, id string) (string, error) {
	c, err := s.get(ctx, id)
	if err != nil {
		return "", err
	}
	var nodes []agents.SwarmNode
	if v, ok := s.seenOf(id); ok {
		nodes = v.nodes
	}
	var last error
	for _, h := range s.readers(c, nodes) {
		// An offline agent's daemon info is its last hello.
		if h != "local" && s.hosts.JoinedAt(h).IsZero() {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, hostTimeout)
		info, err := s.hosts.CurrentDaemonInfo(hctx, h)
		cancel()
		if err != nil {
			last = err
			continue
		}
		if isLinkedManager(c, h, info) {
			s.setReader(id, h)
			return h, nil
		}
	}
	if last != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreadable, last)
	}
	return "", ErrUnreadable
}

// verifiedManagers counts c's manager members that may take over reads.
func verifiedManagers(c Cluster) int {
	n := 0
	for _, m := range c.Members {
		if m.Role == RoleManager && m.Verified {
			n++
		}
	}
	return n
}

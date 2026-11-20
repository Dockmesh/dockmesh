// Package clusters keeps the Docker Swarms Dockmesh manages and which of
// its hosts are their nodes. A cluster exists only after a user created
// it from one of its manager hosts; a host is linked only when a manager's
// node list contains the node ID the host reports, so a host's own claim
// never makes it a member.
package clusters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/host"
)

var (
	ErrExists     = errors.New("a cluster for this Swarm already exists")
	ErrNameTaken  = errors.New("a cluster with this name already exists")
	ErrBadName    = errors.New("invalid name: 1 to 64 letters, digits, spaces, '.', '-' or '_'")
	ErrNotManager = errors.New("the host is not a manager of an active Swarm")
	ErrNotFound   = errors.New("cluster not found")
)

const (
	RoleManager = "manager"
	RoleWorker  = "worker"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

// Hosts is what the service needs from the host registry.
type Hosts interface {
	List(ctx context.Context) ([]host.Info, error)
	// DaemonInfo is the cheap, possibly stale view (last hello).
	DaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error)
	// CurrentDaemonInfo asks online hosts live.
	CurrentDaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error)
	SwarmNodes(ctx context.Context, id string) ([]agents.SwarmNode, error)
}

type Cluster struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OriginHost string    `json:"origin_host"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Members    []Member  `json:"members"`
}

// Member is a host linked to a cluster.
type Member struct {
	HostID   string    `json:"host_id"`
	NodeID   string    `json:"node_id"`
	Role     string    `json:"role"`
	LinkedAt time.Time `json:"linked_at"`
}

// Candidate is a Swarm that has a manager host but no cluster yet.
type Candidate struct {
	ClusterID string          `json:"cluster_id"`
	Managers  []CandidateHost `json:"managers"`
}

type CandidateHost struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	NodeID   string `json:"node_id"`
}

type Service struct {
	db    *sql.DB
	hosts Hosts
	// mu serialises Create and Reconcile, which both rewrite memberships.
	mu sync.Mutex
}

func New(db *sql.DB, hosts Hosts) *Service {
	return &Service{db: db, hosts: hosts}
}

// List returns every cluster with its members, by name.
func (s *Service) List(ctx context.Context) ([]Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, origin_host, COALESCE(created_by, ''), created_at FROM clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []Cluster
	idx := map[string]int{}
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.ID, &c.Name, &c.OriginHost, &c.CreatedBy, &c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		c.Members = []Member{}
		idx[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := s.members(ctx)
	if err != nil {
		return nil, err
	}
	for cid, ms := range members {
		if i, ok := idx[cid]; ok {
			out[i].Members = ms
		}
	}
	return out, nil
}

// members returns cluster id → members, by host id.
func (s *Service) members(ctx context.Context) (map[string][]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, host_id, node_id, role, linked_at FROM cluster_memberships ORDER BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Member{}
	for rows.Next() {
		var cid string
		var m Member
		if err := rows.Scan(&cid, &m.HostID, &m.NodeID, &m.Role, &m.LinkedAt); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], m)
	}
	return out, rows.Err()
}

func (s *Service) exists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clusters WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// Candidates lists Swarms with at least one manager host and no cluster,
// from the hosts' last reported state.
func (s *Service) Candidates(ctx context.Context) ([]Candidate, error) {
	hosts, err := s.hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	byCluster := map[string][]CandidateHost{}
	for _, h := range hosts {
		info, err := s.hosts.DaemonInfo(ctx, h.ID)
		if err != nil || !activeManager(info) {
			continue
		}
		sw := info.Swarm
		byCluster[sw.ClusterID] = append(byCluster[sw.ClusterID], CandidateHost{HostID: h.ID, HostName: h.Name, NodeID: sw.NodeID})
	}
	out := []Candidate{}
	for cid, managers := range byCluster {
		ok, err := s.exists(ctx, cid)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, Candidate{ClusterID: cid, Managers: managers})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClusterID < out[j].ClusterID })
	return out, nil
}

func activeManager(info *agents.DaemonInfo) bool {
	return info != nil && info.Swarm != nil && info.Swarm.NodeState == "active" &&
		info.Swarm.Role == RoleManager && info.Swarm.ClusterID != "" && info.Swarm.NodeID != ""
}

// Create creates the cluster of the Swarm hostID is a manager of and links
// the hosts its node list confirms. The host's role is checked live
// against its own node list, not taken from its last hello.
func (s *Service) Create(ctx context.Context, hostID, name, actor string) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.hosts.CurrentDaemonInfo(ctx, hostID)
	if err != nil {
		return Cluster{}, err
	}
	if !activeManager(info) {
		return Cluster{}, ErrNotManager
	}
	id := info.Swarm.ClusterID
	nodes, err := s.hosts.SwarmNodes(ctx, hostID)
	if err != nil {
		return Cluster{}, fmt.Errorf("list the Swarm's nodes: %w", err)
	}
	if !listsManager(nodes, info.Swarm.NodeID) {
		return Cluster{}, ErrNotManager
	}
	if name == "" {
		name = "swarm-" + id[:min(8, len(id))]
	}
	if !namePattern.MatchString(name) {
		return Cluster{}, ErrBadName
	}
	if ok, err := s.exists(ctx, id); err != nil {
		return Cluster{}, err
	} else if ok {
		return Cluster{}, ErrExists
	}
	var taken int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clusters WHERE name = ?`, name).Scan(&taken); err != nil {
		return Cluster{}, err
	}
	if taken > 0 {
		return Cluster{}, ErrNameTaken
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO clusters (id, name, origin_host, created_by) VALUES (?, ?, ?, ?)`,
		id, name, hostID, nullable(actor)); err != nil {
		return Cluster{}, err
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Cluster{}, err
	}
	if err := s.link(ctx, id, nodes, snap); err != nil {
		return Cluster{}, err
	}
	return s.get(ctx, id)
}

func (s *Service) get(ctx context.Context, id string) (Cluster, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Cluster{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Cluster{}, ErrNotFound
}

func listsManager(nodes []agents.SwarmNode, nodeID string) bool {
	for _, n := range nodes {
		if n.ID == nodeID && n.Role == RoleManager {
			return true
		}
	}
	return false
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// hostState is one host's Swarm claim during a reconcile; known is false
// when its state could not be read, so its membership is left alone.
type hostState struct {
	known bool
	swarm *agents.SwarmState
}

// snapshot reads every host's Swarm state once.
func (s *Service) snapshot(ctx context.Context) (map[string]hostState, error) {
	hosts, err := s.hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]hostState, len(hosts))
	for _, h := range hosts {
		info, err := s.hosts.CurrentDaemonInfo(ctx, h.ID)
		if err != nil {
			out[h.ID] = hostState{}
			continue
		}
		st := hostState{known: true}
		if info != nil {
			st.swarm = info.Swarm
		}
		out[h.ID] = st
	}
	return out, nil
}

// Reconcile refreshes the memberships of every cluster from a manager's
// node list. A cluster whose managers can't be asked keeps its last known
// members.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clusters, err := s.List(ctx)
	if err != nil || len(clusters) == 0 {
		return err
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		nodes := s.askManagers(ctx, c, snap)
		if nodes == nil {
			continue
		}
		if err := s.link(ctx, c.ID, nodes, snap); err != nil {
			return fmt.Errorf("cluster %s: %w", c.Name, err)
		}
	}
	return nil
}

// askManagers returns the node list of the first linked manager (the
// origin host first) that still is a manager of this Swarm, or nil.
func (s *Service) askManagers(ctx context.Context, c Cluster, snap map[string]hostState) []agents.SwarmNode {
	candidates := []string{c.OriginHost}
	for _, m := range c.Members {
		if m.Role == RoleManager && m.HostID != c.OriginHost {
			candidates = append(candidates, m.HostID)
		}
	}
	for _, id := range candidates {
		st := snap[id]
		if !st.known || st.swarm == nil || st.swarm.ClusterID != c.ID || st.swarm.Role != RoleManager {
			continue
		}
		nodes, err := s.hosts.SwarmNodes(ctx, id)
		if err != nil {
			slog.Debug("swarm node list failed", "cluster", c.ID, "host", id, "err", err)
			continue
		}
		if listsManager(nodes, st.swarm.NodeID) {
			return nodes
		}
	}
	return nil
}

// link makes the cluster's memberships match nodes: a host is a member
// when it reports this cluster and a node ID the list contains. Rows are
// only written when they change. Two hosts claiming one node ID keep the
// one already linked; a new tie links neither.
func (s *Service) link(ctx context.Context, clusterID string, nodes []agents.SwarmNode, snap map[string]hostState) error {
	byNode := make(map[string]agents.SwarmNode, len(nodes))
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	existing, err := s.membersOf(ctx, clusterID)
	if err != nil {
		return err
	}
	claims := map[string][]string{}
	for hostID, st := range snap {
		sw := st.swarm
		if !st.known || sw == nil || sw.NodeState != "active" || sw.ClusterID != clusterID {
			continue
		}
		if _, ok := byNode[sw.NodeID]; ok {
			claims[sw.NodeID] = append(claims[sw.NodeID], hostID)
		}
	}
	want := map[string]Member{}
	for nodeID, hostIDs := range claims {
		pick := ""
		if len(hostIDs) == 1 {
			pick = hostIDs[0]
		} else {
			for _, h := range hostIDs {
				if m, ok := existing[h]; ok && m.NodeID == nodeID {
					pick = h
				}
			}
			slog.Warn("several hosts report the same Swarm node ID", "cluster", clusterID, "node", nodeID, "hosts", hostIDs, "linked", pick)
		}
		if pick != "" {
			want[pick] = Member{HostID: pick, NodeID: nodeID, Role: byNode[nodeID].Role}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for hostID, m := range existing {
		st, inSnap := snap[hostID]
		if _, keep := want[hostID]; keep || (inSnap && !st.known) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cluster_memberships WHERE host_id = ? AND cluster_id = ?`, hostID, clusterID); err != nil {
			return err
		}
		slog.Info("host unlinked from cluster", "cluster", clusterID, "host", hostID, "node", m.NodeID)
	}
	for hostID, m := range want {
		if old, ok := existing[hostID]; ok && old.NodeID == m.NodeID && old.Role == m.Role {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cluster_memberships WHERE host_id = ? OR (cluster_id = ? AND node_id = ?)`,
			hostID, clusterID, m.NodeID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cluster_memberships (host_id, cluster_id, node_id, role) VALUES (?, ?, ?, ?)`,
			hostID, clusterID, m.NodeID, m.Role); err != nil {
			return err
		}
		slog.Info("host linked to cluster", "cluster", clusterID, "host", hostID, "node", m.NodeID, "role", m.Role)
	}
	return tx.Commit()
}

func (s *Service) membersOf(ctx context.Context, clusterID string) (map[string]Member, error) {
	all, err := s.members(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Member{}
	for _, m := range all[clusterID] {
		out[m.HostID] = m
	}
	return out, nil
}

// Run reconciles every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("cluster reconcile failed", "err", err)
			}
		}
	}
}

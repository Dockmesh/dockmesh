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
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
)

var (
	ErrExists     = errors.New("a cluster for this Swarm already exists")
	ErrNameTaken  = errors.New("a cluster with this name already exists")
	ErrBadName    = errors.New("invalid name: 1 to 64 letters, digits, spaces, '.', '-' or '_'")
	ErrNotManager = errors.New("the host is not a manager of an active Swarm")
	ErrNotFound   = errors.New("cluster not found")
	// ErrUnreadable: the cluster's origin is offline or no longer the
	// manager node it was linked as.
	ErrUnreadable = errors.New("the cluster's origin manager can't be read right now")
)

// ConflictError refuses a create because Host, another host claiming to
// manage the Swarm, does not list the chosen host as a manager.
type ConflictError struct{ Host string }

func (e *ConflictError) Error() string {
	return "host " + e.Host + " also reports to manage this Swarm but does not list the chosen host as a manager; check or revoke it"
}

func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// ErrConflict matches every *ConflictError.
var ErrConflict = errors.New("swarm managers disagree")

// hostTimeout bounds each live call to one host, so an agent that never
// answers can't stall a reconcile or a create.
const hostTimeout = 5 * time.Second

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
	// SwarmNodeList is a manager's node list and the trust root it reports.
	SwarmNodeList(ctx context.Context, id string) (agents.SwarmNodesResp, error)
	// SwarmProve asks an agent host to sign a node identity proof.
	SwarmProve(ctx context.Context, id string, req agents.SwarmProveReq) (agents.SwarmProveResp, error)
	// JoinedAt is when the agent's current session began, zero if offline.
	JoinedAt(id string) time.Time
	// RemoteIP is the agent session's peer address, "" if unknown.
	RemoteIP(id string) string
}

type Cluster struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OriginHost string    `json:"origin_host"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Members    []Member  `json:"members"`
	Status     *Status   `json:"status,omitempty"`
	Trust      Trust     `json:"trust"`

	trustRoot, trustPending string
}

// Trust is the state of the root CA node proofs are checked against.
// Pending is set while the managers report a different root that is not
// accepted yet; Confirmable when that waits for an admin because the Swarm
// has a single manager.
type Trust struct {
	Pinned bool `json:"pinned"`
	// The fingerprints are SHA-256 over the root certificate, as
	// openssl x509 -fingerprint -sha256 prints it for swarm-root-ca.crt.
	PinnedFingerprint  string     `json:"pinned_fingerprint,omitempty"`
	PendingFingerprint string     `json:"pending_fingerprint,omitempty"`
	Pending            bool       `json:"pending,omitempty"`
	PendingSince       *time.Time `json:"pending_since,omitempty"`
	Confirmable        bool       `json:"confirmable,omitempty"`
	OriginVerified     bool       `json:"origin_verified"`
}

// Member is a host linked to a cluster. Verified is set while the host's
// last proof of the node's key holds; Proof says why it isn't.
type Member struct {
	HostID       string     `json:"host_id"`
	NodeID       string     `json:"node_id"`
	Role         string     `json:"role"`
	LinkedAt     time.Time  `json:"linked_at"`
	Verified     bool       `json:"verified"`
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	CertNotAfter *time.Time `json:"cert_not_after,omitempty"`
	Proof        string     `json:"proof"`
	// AddrWarning: the agent connects from another address than the one
	// the Swarm lists for the node. Only a hint; NAT and proxies cause it.
	AddrWarning string `json:"addr_warning,omitempty"`

	certIssuer string
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
	audit Auditor
	// mu serialises the membership writes of Create, Reconcile and Delete.
	mu sync.Mutex
	// smu guards seen, the last known status per cluster.
	smu      sync.Mutex
	seen     map[string]seen
	loadOnce sync.Once
	// wmu orders the writes of seen to cluster_status, so smu isn't held
	// across the I/O.
	wmu sync.Mutex
	// serverID is bound into every proof; see SetServerID.
	serverID string
	notices  Noticer
	// pmu guards proofs and addrWarn, per host.
	pmu      sync.Mutex
	proofs   map[string]proofTry
	addrWarn map[string]string
	// sole: the cluster's origin was its only manager at the last check.
	sole map[string]bool
	// reader is the host each cluster is read through (see readers).
	reader map[string]string
	now  func() time.Time
}

// Auditor records membership changes; audit.Service is one.
type Auditor interface {
	Write(ctx context.Context, userID, action, target string, details any)
}

// SetAudit sets where membership changes are recorded.
func (s *Service) SetAudit(a Auditor) { s.audit = a }

func New(db *sql.DB, hosts Hosts) *Service {
	return &Service{db: db, hosts: hosts, now: time.Now}
}

// List returns every cluster with its members, by name.
func (s *Service) List(ctx context.Context) ([]Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, origin_host, COALESCE(created_by, ''), created_at,
		COALESCE(trust_root, ''), COALESCE(trust_root_pending, ''), trust_root_pending_at FROM clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []Cluster{}
	idx := map[string]int{}
	for rows.Next() {
		var c Cluster
		var pendingAt sql.NullTime
		if err := rows.Scan(&c.ID, &c.Name, &c.OriginHost, &c.CreatedBy, &c.CreatedAt, &c.trustRoot, &c.trustPending, &pendingAt); err != nil {
			rows.Close()
			return nil, err
		}
		c.Trust.Pinned = c.trustRoot != ""
		c.Trust.Pending = c.trustPending != ""
		c.Trust.PinnedFingerprint = rootFingerprint(c.trustRoot)
		c.Trust.PendingFingerprint = rootFingerprint(c.trustPending)
		if c.Trust.Pending && pendingAt.Valid {
			t := pendingAt.Time
			c.Trust.PendingSince = &t
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
	for i := range out {
		s.annotate(&out[i])
		out[i].Status = s.statusOf(out[i])
	}
	return out, nil
}

// members returns cluster id → members, by host id.
func (s *Service) members(ctx context.Context) (map[string][]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, host_id, node_id, role, linked_at,
		verified_at, cert_not_after, COALESCE(cert_issuer, '') FROM cluster_memberships ORDER BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Member{}
	for rows.Next() {
		var cid string
		var m Member
		var verifiedAt, notAfter sql.NullTime
		if err := rows.Scan(&cid, &m.HostID, &m.NodeID, &m.Role, &m.LinkedAt, &verifiedAt, &notAfter, &m.certIssuer); err != nil {
			return nil, err
		}
		if verifiedAt.Valid {
			t := verifiedAt.Time
			m.VerifiedAt = &t
		}
		if notAfter.Valid {
			t := notAfter.Time
			m.CertNotAfter = &t
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
		if !enrolled(h) {
			continue
		}
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

// enrolled reports whether h is a working host: revoked, removed and
// pending agents take no part in clusters.
func enrolled(h host.Info) bool {
	return h.Status != "revoked" && h.Status != "removed" && h.Status != "pending"
}

func activeManager(info *agents.DaemonInfo) bool {
	return info != nil && info.Swarm != nil && info.Swarm.NodeState == "active" &&
		info.Swarm.Role == RoleManager && info.Swarm.ClusterID != "" && info.Swarm.NodeID != ""
}

func (s *Service) nodesOf(ctx context.Context, hostID string) (agents.SwarmNodesResp, error) {
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	return s.hosts.SwarmNodeList(ctx, hostID)
}

// Create creates the cluster of the Swarm hostID is a manager of and links
// the hosts its node list confirms. The host's role is checked live
// against its own node list, and every other host that claims to manage
// the same Swarm and answers must list it as a manager too. The host
// becomes the cluster's origin: only its node list changes memberships.
func (s *Service) Create(ctx context.Context, hostID, name, actor string) (Cluster, error) {
	if name != "" && !namePattern.MatchString(name) {
		return Cluster{}, ErrBadName
	}
	info, err := s.hosts.CurrentDaemonInfo(ctx, hostID)
	if err != nil {
		return Cluster{}, err
	}
	if !activeManager(info) {
		return Cluster{}, ErrNotManager
	}
	id, nodeID := info.Swarm.ClusterID, info.Swarm.NodeID
	list, err := s.nodesOf(ctx, hostID)
	if err != nil {
		return Cluster{}, fmt.Errorf("list the Swarm's nodes: %w", err)
	}
	nodes, root := list.Nodes, canonicalRoot(list.TrustRoot)
	if !listsManager(nodes, nodeID) {
		return Cluster{}, ErrNotManager
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Cluster{}, err
	}
	agreed, blocked := 0, false
	for other, st := range snap {
		if other == hostID || !st.known || st.swarm == nil || st.swarm.ClusterID != id || st.swarm.Role != RoleManager {
			continue
		}
		theirs, err := s.nodesOf(ctx, other)
		if err != nil {
			continue
		}
		if !listsManager(theirs.Nodes, nodeID) {
			slog.Warn("swarm managers disagree", "cluster", id, "origin", hostID, "other", other)
			return Cluster{}, &ConflictError{Host: other}
		}
		if !listsManager(nodes, st.swarm.NodeID) {
			// The origin leaves out a host that says it manages the
			// Swarm: don't take the origin's word for the root.
			blocked = true
			continue
		}
		r := canonicalRoot(theirs.TrustRoot)
		if r == "" || root == "" {
			continue
		}
		if r != root {
			slog.Warn("swarm managers report different trust roots", "cluster", id, "origin", hostID, "other", other)
			return Cluster{}, &ConflictError{Host: other}
		}
		agreed++
	}
	if blocked || !pinnable(agreed, soleManager(nodes, snap, id, hostID)) {
		root = ""
	}
	if name == "" {
		name = "swarm-" + id[:min(8, len(id))]
	}
	if err := s.insert(ctx, id, name, hostID, actor, root, nodes, snap); err != nil {
		return Cluster{}, err
	}
	c, err := s.get(ctx, id)
	if err != nil {
		return Cluster{}, err
	}
	s.prove(ctx, c, nodes, snap, false)
	return s.get(ctx, id)
}

// insert stores a new cluster and links its first members.
func (s *Service) insert(ctx context.Context, id, name, hostID, actor, root string, nodes []agents.SwarmNode, snap map[string]hostState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok, err := s.exists(ctx, id); err != nil {
		return err
	} else if ok {
		return ErrExists
	}
	var taken int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clusters WHERE name = ?`, name).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return ErrNameTaken
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO clusters (id, name, origin_host, created_by, trust_root) VALUES (?, ?, ?, ?, ?)`,
		id, name, hostID, nullable(actor), nullable(root)); err != nil {
		return err
	}
	if err := s.link(ctx, id, hostID, nodes, snap); err != nil {
		return err
	}
	s.record(id, stateOf(nodes), nodes, time.Now())
	return nil
}

// Delete removes a cluster, its memberships and everything granting or
// restricting access on its scope (see Access), in one transaction;
// nothing changes on the Swarm. It is the way out of a cluster created
// from the wrong host. allow sees what would go, inside the transaction,
// and stops the delete by returning an error. The caller reloads the role
// cache.
func (s *Service) Delete(ctx context.Context, id string, allow func(Access) error) (Cluster, Access, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.get(ctx, id)
	if err != nil {
		return Cluster{}, Access{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Cluster{}, Access{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, invites, err := accessOf(ctx, tx, id)
	if err != nil {
		return Cluster{}, Access{}, err
	}
	if allow != nil {
		if err := allow(a); err != nil {
			return Cluster{}, Access{}, err
		}
	}
	if err := removeAccess(ctx, tx, id, invites); err != nil {
		return Cluster{}, Access{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM clusters WHERE id = ?`, id); err != nil {
		return Cluster{}, Access{}, err
	}
	if err := tx.Commit(); err != nil {
		return Cluster{}, Access{}, err
	}
	s.forget(id)
	s.resolveNotices(ctx, id)
	return c, a, nil
}

// Get returns one cluster with its members, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (Cluster, error) { return s.get(ctx, id) }

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
	// offline: the state is the host's last hello, not a live answer.
	offline bool
}

// snapshot reads the Swarm state of every enrolled host once, several
// hosts at a time. Hosts that are not enrolled are left out, so their
// memberships end.
func (s *Service) snapshot(ctx context.Context) (map[string]hostState, error) {
	hosts, err := s.hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	out := make(map[string]hostState, len(hosts))
	for _, h := range hosts {
		if !enrolled(h) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string, offline bool) {
			defer func() { <-sem; wg.Done() }()
			hctx, cancel := context.WithTimeout(ctx, hostTimeout)
			defer cancel()
			st := hostState{offline: offline}
			if info, err := s.hosts.CurrentDaemonInfo(hctx, id); err == nil {
				st.known = true
				if info != nil {
					st.swarm = info.Swarm
				}
			}
			mu.Lock()
			out[id] = st
			mu.Unlock()
		}(h.ID, h.Status == "offline")
	}
	wg.Wait()
	return out, nil
}

// Reconcile refreshes the memberships of every cluster from its origin's
// node list. A cluster whose origin can't be asked, or no longer is the
// manager node it was linked as, keeps its last known members.
func (s *Service) Reconcile(ctx context.Context) error {
	clusters, err := s.List(ctx)
	if err != nil || len(clusters) == 0 {
		return err
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		list, state, by := s.askManagers(ctx, c, snap)
		nodes := list.Nodes
		s.record(c.ID, state, nodes, time.Now())
		if nodes == nil {
			continue
		}
		s.mu.Lock()
		err := s.link(ctx, c.ID, by, nodes, snap)
		s.mu.Unlock()
		if err != nil {
			return fmt.Errorf("cluster %s: %w", c.Name, err)
		}
		if err := s.reconcileIdentity(ctx, c.ID, by, list, snap); err != nil {
			return fmt.Errorf("cluster %s: %w", c.Name, err)
		}
	}
	return nil
}

// linkedNode returns the node ID c's origin is linked as, or "".
func linkedNode(c Cluster) string { return nodeOf(c, c.OriginHost) }

// nodeOf returns the node ID host is linked as in c, or "".
func nodeOf(c Cluster, host string) string {
	for _, m := range c.Members {
		if m.HostID == host {
			return m.NodeID
		}
	}
	return ""
}

// isLinkedManager reports whether info shows host as the active manager
// node of c it is linked as.
func isLinkedManager(c Cluster, host string, info *agents.DaemonInfo) bool {
	linked := nodeOf(c, host)
	return linked != "" && activeManager(info) && info.Swarm.ClusterID == c.ID && info.Swarm.NodeID == linked
}

// claims reports whether a host's Swarm state claims node n of cluster
// clusterID. dockerd reports the cluster ID only on managers, so a worker
// claims by node ID alone.
func claims(sw *agents.SwarmState, clusterID string, n agents.SwarmNode) bool {
	if sw == nil || sw.NodeState != "active" || sw.NodeID != n.ID {
		return false
	}
	if sw.ClusterID == clusterID {
		return true
	}
	return sw.ClusterID == "" && sw.Role == RoleWorker && n.Role == RoleWorker
}

// link makes the cluster's memberships match nodes, the node list of
// listedBy: a host is a member when its state claims a node the list
// contains; the role is the list's. Rows are only written when they
// change. Two hosts claiming one node keep listedBy or the one already
// linked; any other tie links neither.
func (s *Service) link(ctx context.Context, clusterID, listedBy string, nodes []agents.SwarmNode, snap map[string]hostState) error {
	byNode := make(map[string]agents.SwarmNode, len(nodes))
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	existing, err := s.membersOf(ctx, clusterID)
	if err != nil {
		return err
	}
	claimants := map[string][]string{}
	for hostID, st := range snap {
		if !st.known || st.swarm == nil {
			continue
		}
		if n, ok := byNode[st.swarm.NodeID]; ok && claims(st.swarm, clusterID, n) {
			claimants[n.ID] = append(claimants[n.ID], hostID)
		}
	}
	want := map[string]Member{}
	for nodeID, hostIDs := range claimants {
		pick := ""
		switch {
		case len(hostIDs) == 1:
			pick = hostIDs[0]
		case slices.Contains(hostIDs, listedBy):
			// The list's own host was checked live against it.
			pick = listedBy
		default:
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

	type change struct {
		linked bool
		m      Member
	}
	var changes []change
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
		changes = append(changes, change{false, m})
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
		changes = append(changes, change{true, m})
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, c := range changes {
		slog.Info("cluster membership changed", "linked", c.linked, "cluster", clusterID, "host", c.m.HostID, "node", c.m.NodeID, "role", c.m.Role)
		if s.audit == nil {
			continue
		}
		details := map[string]any{"host": c.m.HostID, "node": c.m.NodeID, "role": c.m.Role, "listed_by": listedBy}
		if c.linked {
			s.audit.Write(ctx, "", audit.ActionClusterLink, clusterID, details)
		} else {
			s.audit.Write(ctx, "", audit.ActionClusterUnlink, clusterID, details)
		}
	}
	return nil
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

// ManagerOf returns the id of the cluster hostID is linked to as a
// manager, or "".
func (s *Service) ManagerOf(ctx context.Context, hostID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT cluster_id FROM cluster_memberships WHERE host_id = ? AND role = ?`, hostID, RoleManager).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
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

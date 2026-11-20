package clusters

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Proof states of a member, as the API reports them.
const (
	ProofVerified    = "verified"
	ProofLocal       = "local" // the server's own host, trusted without a proof
	ProofPending     = "pending"
	ProofAgentOld    = "agent_too_old"
	ProofNoTrustRoot = "no_trust_root"
	ProofKeyLocked   = "key_locked"
	ProofInvalid     = "invalid"
	ProofFailed      = "failed"
)

const (
	// proofEvery re-asks a verified host even when nothing changed.
	proofEvery = 24 * time.Hour
	// proofRenew re-asks before the proven certificate expires; dockerd
	// renews node certificates long before that.
	proofRenew = 24 * time.Hour
	// proofRetry spaces out attempts at a host whose last proof failed.
	proofRetry = 10 * time.Minute
	// proofTimeout covers a helper image pull on the agent.
	proofTimeout = 45 * time.Second
)

// ErrNoPendingRoot: there is no changed trust root to confirm.
var ErrNoPendingRoot = errors.New("the cluster has no changed trust root to confirm")

// ErrConfirmNeedsManagers: only a Swarm with a single manager takes an
// admin's confirmation; with several, two verified managers must agree.
var ErrConfirmNeedsManagers = errors.New("a new trust root can be confirmed only in a Swarm with a single manager; with several, two verified managers must report it")

// ErrRootChanged: the pending root is not the one the caller confirmed.
var ErrRootChanged = errors.New("the pending root CA is not the one you confirmed; compare its fingerprint again")

// Noticer raises and resolves action-required notices.
type Noticer interface {
	Set(ctx context.Context, active bool, in notices.RaiseInput) error
}

// SetServerID sets the id bound into every proof request, so a proof made
// for another Dockmesh server is worthless here.
func (s *Service) SetServerID(id string) { s.serverID = id }

// SetNotices sets where the cluster notices go.
func (s *Service) SetNotices(n Noticer) { s.notices = n }

// proofTry is the outcome of the last proof request to one host.
type proofTry struct {
	at   time.Time
	code string
}

// proofStale is how long a proof counts without being renewed: a host
// that stops answering loses it, even while its certificate is valid.
const proofStale = proofEvery + 2*time.Hour

// verifiedNow reports whether m's last proof holds at now.
func verifiedNow(m Member, now time.Time) bool {
	return m.VerifiedAt != nil && now.Sub(*m.VerifiedAt) < proofStale && (m.CertNotAfter == nil || now.Before(*m.CertNotAfter))
}

// annotate fills the proof fields of c's members and its trust summary.
func (s *Service) annotate(c *Cluster) {
	now := s.now()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	for i := range c.Members {
		m := &c.Members[i]
		m.AddrWarning = s.addrWarn[m.HostID]
		switch {
		case m.HostID == "local":
			m.Verified, m.Proof = true, ProofLocal
		case verifiedNow(*m, now):
			m.Verified, m.Proof = true, ProofVerified
		case s.proofs[m.HostID].code != "":
			m.Proof = s.proofs[m.HostID].code
		default:
			m.Proof = ProofPending
		}
		if m.HostID == c.OriginHost {
			c.Trust.OriginVerified = m.Verified
		}
	}
	c.Trust.Confirmable = c.Trust.Pending && s.sole[c.ID]
}

// pinnable reports whether a root the origin reports may be pinned: the
// origin alone could make up a root and prove itself against it, so a
// second manager host must report the same root unless the Swarm has a
// single manager.
func pinnable(agreed int, sole bool) bool {
	return agreed > 0 || sole
}

// otherManagers returns the hosts besides origin whose own daemon says it
// manages cluster id, offline ones by their last hello.
func otherManagers(snap map[string]hostState, id, origin string) []string {
	var out []string
	for h, st := range snap {
		if h != origin && st.known && st.swarm != nil && st.swarm.ClusterID == id && st.swarm.Role == RoleManager {
			out = append(out, h)
		}
	}
	return out
}

// soleManager reports whether the origin is the Swarm's only manager. The
// origin's node list alone can't tell, since the origin may leave managers
// out, so no other host may claim to manage the Swarm either.
func soleManager(nodes []agents.SwarmNode, snap map[string]hostState, id, origin string) bool {
	return managersIn(nodes) == 1 && len(otherManagers(snap, id, origin)) == 0
}

func managersIn(nodes []agents.SwarmNode) int {
	n := 0
	for _, x := range nodes {
		if x.Role == RoleManager {
			n++
		}
	}
	return n
}

// reconcileIdentity pins or rotates the trust root from the origin's list,
// proves the members that are due and refreshes the cluster's notices.
func (s *Service) reconcileIdentity(ctx context.Context, id, by string, list agents.SwarmNodesResp, snap map[string]hostState) error {
	c, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	s.pmu.Lock()
	if s.sole == nil {
		s.sole = map[string]bool{}
	}
	s.sole[id] = soleManager(list.Nodes, snap, id, by)
	s.pmu.Unlock()
	rotated, err := s.checkTrust(ctx, c, by, canonicalRoot(list.TrustRoot), list.Nodes, snap)
	if err != nil {
		return err
	}
	if c, err = s.get(ctx, id); err != nil {
		return err
	}
	s.prove(ctx, c, list.Nodes, snap, rotated)
	s.warnAddrs(c, list.Nodes)
	if c, err = s.get(ctx, id); err != nil {
		return err
	}
	s.refreshNotices(ctx, c)
	return nil
}

// checkTrust compares the root the origin reports with the pinned one.
// An unpinned cluster pins it when no other manager host reports another
// root. A changed root waits as pending and replaces the pinned one once
// two verified managers report it. rotated reports a replacement.
func (s *Service) checkTrust(ctx context.Context, c Cluster, by, reported string, nodes []agents.SwarmNode, snap map[string]hostState) (rotated bool, err error) {
	if reported == "" {
		return false, nil
	}
	switch {
	case c.trustRoot == "":
		var agreed []string
		for _, other := range otherManagers(snap, c.ID, by) {
			st := snap[other]
			if !listsManager(nodes, st.swarm.NodeID) {
				slog.Warn("a host claims to manage the Swarm but the origin does not list it; not pinning", "cluster", c.ID, "origin", by, "other", other)
				return false, nil
			}
			if st.offline {
				continue
			}
			theirs, err := s.nodesOf(ctx, other)
			if err != nil {
				continue
			}
			switch r := canonicalRoot(theirs.TrustRoot); {
			case r == reported:
				agreed = append(agreed, other)
			case r != "":
				slog.Warn("swarm managers report different trust roots; not pinning", "cluster", c.ID, "origin", by, "other", other)
				return false, nil
			}
		}
		if !pinnable(len(agreed), soleManager(nodes, snap, c.ID, by)) {
			return false, nil
		}
		res, err := s.db.ExecContext(ctx, `UPDATE clusters SET trust_root = ? WHERE id = ? AND trust_root IS NULL`, reported, c.ID)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 && s.audit != nil {
			s.audit.Write(ctx, "", audit.ActionClusterTrustRoot, c.ID, map[string]any{"change": "pinned", "reported_by": by, "agreed_by": agreed})
		}
		return false, nil

	case reported == c.trustRoot:
		if c.trustPending != "" {
			_, err := s.db.ExecContext(ctx, `UPDATE clusters SET trust_root_pending = NULL, trust_root_pending_at = NULL WHERE id = ?`, c.ID)
			return false, err
		}
		return false, nil
	}

	if reported != c.trustPending {
		if _, err := s.db.ExecContext(ctx, `UPDATE clusters SET trust_root_pending = ?, trust_root_pending_at = ? WHERE id = ?`,
			reported, s.now().UTC(), c.ID); err != nil {
			return false, err
		}
		slog.Warn("swarm trust root changed; waiting for two verified managers", "cluster", c.ID)
	}
	agreed := s.agreeingManagers(ctx, c, by, reported, snap)
	if len(agreed) < 2 {
		return false, nil
	}
	if err := s.acceptRoot(ctx, c.ID, reported); err != nil {
		return false, err
	}
	if s.audit != nil {
		s.audit.Write(ctx, "", audit.ActionClusterTrustRoot, c.ID, map[string]any{"change": "rotated", "agreed_by": agreed})
	}
	return true, nil
}

// agreeingManagers returns the verified manager members that are online
// and whose own node list reports root, the origin's report included.
func (s *Service) agreeingManagers(ctx context.Context, c Cluster, by, root string, snap map[string]hostState) []string {
	var out []string
	for _, m := range c.Members {
		if m.Role != RoleManager || !m.Verified {
			continue
		}
		if st := snap[m.HostID]; !st.known || st.offline {
			continue
		}
		if m.HostID != by {
			theirs, err := s.nodesOf(ctx, m.HostID)
			if err != nil || canonicalRoot(theirs.TrustRoot) != root {
				continue
			}
		}
		out = append(out, m.HostID)
	}
	return out
}

func (s *Service) acceptRoot(ctx context.Context, id, root string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE clusters SET trust_root = ?, trust_root_pending = NULL, trust_root_pending_at = NULL WHERE id = ? AND trust_root_pending = ?`,
		root, id, root)
	return err
}

// ConfirmTrustRoot accepts the pending trust root of a Swarm with a single
// manager, where no second manager can agree to it. fingerprint is the one
// the admin compared, so a root that changed meanwhile is refused.
func (s *Service) ConfirmTrustRoot(ctx context.Context, id, fingerprint, actor string) (Cluster, error) {
	c, err := s.get(ctx, id)
	if err != nil {
		return Cluster{}, err
	}
	if !c.Trust.Pending {
		return Cluster{}, ErrNoPendingRoot
	}
	if !c.Trust.Confirmable {
		return Cluster{}, ErrConfirmNeedsManagers
	}
	if fingerprint == "" || !strings.EqualFold(fingerprint, c.Trust.PendingFingerprint) {
		return Cluster{}, ErrRootChanged
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Cluster{}, err
	}
	if others := otherManagers(snap, id, c.OriginHost); len(others) > 0 {
		return Cluster{}, ErrConfirmNeedsManagers
	}
	if err := s.acceptRoot(ctx, id, c.trustPending); err != nil {
		return Cluster{}, err
	}
	if s.audit != nil {
		s.audit.Write(ctx, actor, audit.ActionClusterTrustRoot, id, map[string]any{"change": "confirmed", "fingerprint": c.Trust.PendingFingerprint})
	}
	s.pmu.Lock()
	for _, m := range c.Members {
		delete(s.proofs, m.HostID)
	}
	s.pmu.Unlock()
	if c, err = s.get(ctx, id); err != nil {
		return Cluster{}, err
	}
	s.refreshNotices(ctx, c)
	return c, nil
}

// due reports whether member m should be asked for a proof now.
func (s *Service) due(m Member, n agents.SwarmNode, joined, now time.Time, force bool) bool {
	s.pmu.Lock()
	last := s.proofs[m.HostID]
	s.pmu.Unlock()
	reconnected := !joined.IsZero() && (last.at.IsZero() || joined.After(last.at))
	if last.code != "" && last.code != ProofVerified && !reconnected && !force && now.Sub(last.at) < proofRetry {
		return false
	}
	switch {
	case force, m.VerifiedAt == nil, reconnected && joined.After(*m.VerifiedAt):
		return true
	case now.Sub(*m.VerifiedAt) >= proofEvery:
		return true
	case m.CertNotAfter != nil && m.CertNotAfter.Sub(now) < proofRenew:
		return true
	case n.TLSIssuer != "" && m.certIssuer != "" && n.TLSIssuer != m.certIssuer:
		return true
	}
	return false
}

// prove asks every due, online agent member of c for a node proof, a few
// hosts at a time, and stores the outcome.
func (s *Service) prove(ctx context.Context, c Cluster, nodes []agents.SwarmNode, snap map[string]hostState, force bool) {
	byID := make(map[string]agents.SwarmNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	now := s.now()
	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, 4)
	)
	for _, m := range c.Members {
		if m.HostID == "local" {
			continue
		}
		if st := snap[m.HostID]; !st.known || st.offline {
			continue
		}
		n, listed := byID[m.NodeID]
		if !listed || !s.due(m, n, s.hosts.JoinedAt(m.HostID), now, force) {
			continue
		}
		if c.trustRoot == "" {
			s.setTry(m.HostID, ProofNoTrustRoot, now)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(m Member) {
			defer func() { <-sem; wg.Done() }()
			s.proveOne(ctx, c, m)
		}(m)
	}
	wg.Wait()
}

func (s *Service) setTry(hostID, code string, at time.Time) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.proofs == nil {
		s.proofs = map[string]proofTry{}
	}
	s.proofs[hostID] = proofTry{at: at, code: code}
}

// proveOne runs one challenge against m's agent. The nonce lives only in
// this call, so an answer is never accepted twice.
func (s *Service) proveOne(ctx context.Context, c Cluster, m Member) {
	req := agents.SwarmProveReq{Nonce: make([]byte, agents.ProofNonceSize), ServerID: s.serverID, HostID: m.HostID}
	if _, err := rand.Read(req.Nonce); err != nil {
		slog.Error("swarm proof nonce", "err", err)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, proofTimeout)
	defer cancel()
	resp, err := s.hosts.SwarmProve(pctx, m.HostID, req)
	now := s.now()
	if err != nil {
		code, definite := ProofFailed, true
		switch {
		case errors.Is(err, host.ErrAgentNoSwarmProve):
			code = ProofAgentOld
		case errors.Is(err, host.ErrProofRefused) && strings.Contains(err.Error(), "autolock"):
			code = ProofKeyLocked
		case !errors.Is(err, host.ErrProofRefused):
			// Not an answer from the agent: keep the last proof.
			definite = false
		}
		slog.Info("swarm node proof failed", "cluster", c.ID, "host", m.HostID, "node", m.NodeID, "err", err)
		s.setTry(m.HostID, code, now)
		if definite {
			s.unverify(ctx, c.ID, m, code)
		}
		return
	}
	v, err := verifyProof(c.trustRoot, req, resp, c.ID, m.NodeID, m.Role, now)
	if err != nil {
		slog.Warn("swarm node proof refused", "cluster", c.ID, "host", m.HostID, "node", m.NodeID, "err", err)
		s.setTry(m.HostID, ProofInvalid, now)
		if c.Trust.Pending && unknownAuthority(err) {
			return
		}
		s.unverify(ctx, c.ID, m, ProofInvalid)
		return
	}
	res, err := s.db.ExecContext(ctx, `UPDATE cluster_memberships SET verified_at = ?, cert_not_after = ?, cert_issuer = ?
		WHERE host_id = ? AND cluster_id = ? AND node_id = ? AND role = ?`,
		now.UTC(), v.notAfter.UTC(), nullable(v.issuer), m.HostID, c.ID, m.NodeID, m.Role)
	if err != nil {
		slog.Warn("store swarm node proof", "host", m.HostID, "err", err)
		return
	}
	s.setTry(m.HostID, ProofVerified, now)
	if n, _ := res.RowsAffected(); n > 0 && m.VerifiedAt == nil {
		slog.Info("swarm node verified", "cluster", c.ID, "host", m.HostID, "node", m.NodeID, "role", m.Role)
		if s.audit != nil {
			s.audit.Write(ctx, "", audit.ActionClusterNodeVerified, c.ID, map[string]any{"host": m.HostID, "node": m.NodeID, "role": m.Role})
		}
	}
}

func (s *Service) unverify(ctx context.Context, clusterID string, m Member, code string) {
	if m.VerifiedAt == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE cluster_memberships SET verified_at = NULL, cert_not_after = NULL, cert_issuer = NULL
		WHERE host_id = ? AND cluster_id = ? AND node_id = ?`, m.HostID, clusterID, m.NodeID); err != nil {
		slog.Warn("clear swarm node proof", "host", m.HostID, "err", err)
		return
	}
	if s.audit != nil {
		s.audit.Write(ctx, "", audit.ActionClusterNodeUnverified, clusterID, map[string]any{"host": m.HostID, "node": m.NodeID, "reason": code})
	}
}

// warnAddrs compares each agent session's peer address with the address
// the Swarm lists for its node. A difference is only shown, never acted
// on: NAT, proxies and multi-homed hosts produce it legitimately.
func (s *Service) warnAddrs(c Cluster, nodes []agents.SwarmNode) {
	byID := make(map[string]agents.SwarmNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.addrWarn == nil {
		s.addrWarn = map[string]string{}
	}
	for _, m := range c.Members {
		if m.HostID == "local" {
			continue
		}
		n, ok := byID[m.NodeID]
		peer := s.hosts.RemoteIP(m.HostID)
		if !ok || n.Addr == "" || peer == "" || peer == n.Addr {
			delete(s.addrWarn, m.HostID)
			continue
		}
		if s.addrWarn[m.HostID] == "" {
			slog.Warn("agent address differs from the Swarm node address", "cluster", c.ID, "host", m.HostID, "agent_addr", peer, "node_addr", n.Addr)
		}
		s.addrWarn[m.HostID] = fmt.Sprintf("%s|%s", peer, n.Addr)
	}
}

func noVerifiedManagerKey(id string) string { return "clusters.no-verified-manager." + id }
func trustRootKey(id string) string         { return "clusters.trust-root-changed." + id }

// refreshNotices raises a notice while a cluster has manager members but
// none of them is verified, and while a changed trust root waits for an
// admin.
func (s *Service) refreshNotices(ctx context.Context, c Cluster) {
	if s.notices == nil {
		return
	}
	managers, verified := 0, 0
	for _, m := range c.Members {
		if m.Role == RoleManager {
			managers++
			if m.Verified {
				verified++
			}
		}
	}
	attempted := false
	s.pmu.Lock()
	for _, m := range c.Members {
		if m.Role == RoleManager && s.proofs[m.HostID].code != "" {
			attempted = true
		}
	}
	s.pmu.Unlock()
	link := "/clusters/" + c.ID
	aud := notices.WithPermission(string(rbac.PermClustersCreate))
	if err := s.notices.Set(ctx, managers > 0 && verified == 0 && attempted, notices.RaiseInput{
		Key: noVerifiedManagerKey(c.ID), Audience: aud, Severity: notices.SevWarning,
		Title: i18n.Msg("notice.cluster_unverified.title", c.Name),
		Body:  i18n.Msg("notice.cluster_unverified.body", c.Name),
		Link:  link,
	}); err != nil {
		slog.Warn("cluster notice", "cluster", c.ID, "err", err)
	}
	if err := s.notices.Set(ctx, c.Trust.Pending && c.Trust.Confirmable, notices.RaiseInput{
		Key: trustRootKey(c.ID), Audience: aud, Severity: notices.SevWarning,
		Title: i18n.Msg("notice.cluster_trust_root.title", c.Name),
		Body:  i18n.Msg("notice.cluster_trust_root.body", c.Name),
		Link:  link,
	}); err != nil {
		slog.Warn("cluster notice", "cluster", c.ID, "err", err)
	}
}

// resolveNotices ends a deleted cluster's notices.
func (s *Service) resolveNotices(ctx context.Context, id string) {
	if s.notices == nil {
		return
	}
	for _, k := range []string{noVerifiedManagerKey(id), trustRootKey(id)} {
		if err := s.notices.Set(ctx, false, notices.RaiseInput{Key: k}); err != nil {
			slog.Warn("cluster notice", "cluster", id, "err", err)
		}
	}
}

package clusters

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// swarmCA is a root CA shaped like the one swarmkit creates: ECDSA P-256,
// CN swarm-ca, signing node certificates with CN = node ID,
// O = cluster ID and OU = swarm-manager | swarm-worker.
type swarmCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

var serial atomic.Int64

func newSwarmCA(t *testing.T) *swarmCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial.Add(1)),
		Subject:               pkix.Name{CommonName: "swarm-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(20 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &swarmCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// nodeCert is what a node holds in swarm-node.{crt,key}.
type nodeCert struct {
	pem string
	key *ecdsa.PrivateKey
}

func (ca *swarmCA) issue(t *testing.T, nodeID, clusterID, role string, notAfter time.Time) nodeCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)),
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{clusterID}, OrganizationalUnit: []string{swarmOU(role)}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{swarmOU(role), nodeID},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return nodeCert{pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key: key}
}

func (n nodeCert) sign(t *testing.T, req agents.SwarmProveReq) agents.SwarmProveResp {
	t.Helper()
	msg, err := agents.ProofMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(msg)
	sig, err := n.key.Sign(rand.Reader, d[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return agents.SwarmProveResp{CertPEM: n.pem, Signature: sig}
}

// prover answers like an agent holding n.
func (n nodeCert) prover(t *testing.T) func(agents.SwarmProveReq) (agents.SwarmProveResp, error) {
	return func(req agents.SwarmProveReq) (agents.SwarmProveResp, error) { return n.sign(t, req), nil }
}

func proofReq(host string) agents.SwarmProveReq {
	nonce := make([]byte, agents.ProofNonceSize)
	_, _ = rand.Read(nonce)
	return agents.SwarmProveReq{Nonce: nonce, ServerID: "server-1", HostID: host}
}

func TestVerifyProof(t *testing.T) {
	ca, other := newSwarmCA(t), newSwarmCA(t)
	later := time.Now().Add(90 * 24 * time.Hour)
	good := ca.issue(t, "n1", swarmID, RoleManager, later)
	req := proofReq("m1")

	tests := []struct {
		name   string
		resp   func() agents.SwarmProveResp
		check  agents.SwarmProveReq
		root   string
		nodeID string
		role   string
		now    time.Time
		ok     bool
	}{
		{name: "valid", resp: func() agents.SwarmProveResp { return good.sign(t, req) }, ok: true},
		{name: "other node's certificate", resp: func() agents.SwarmProveResp {
			return ca.issue(t, "n3", swarmID, RoleManager, later).sign(t, req)
		}},
		{name: "other Swarm", resp: func() agents.SwarmProveResp { return ca.issue(t, "n1", "otherswarm", RoleManager, later).sign(t, req) }},
		{name: "worker certificate for a manager", resp: func() agents.SwarmProveResp { return ca.issue(t, "n1", swarmID, RoleWorker, later).sign(t, req) }},
		{name: "other CA", resp: func() agents.SwarmProveResp { return other.issue(t, "n1", swarmID, RoleManager, later).sign(t, req) }},
		{name: "self-signed leaf claiming the node", resp: func() agents.SwarmProveResp {
			self := newSwarmCA(t)
			return self.issue(t, "n1", swarmID, RoleManager, later).sign(t, req)
		}},
		{name: "expired", resp: func() agents.SwarmProveResp { return good.sign(t, req) }, now: later.Add(time.Hour)},
		{name: "real certificate, wrong key", resp: func() agents.SwarmProveResp {
			p := ca.issue(t, "n2", swarmID, RoleManager, later).sign(t, req)
			p.CertPEM = good.pem
			return p
		}},
		{name: "replay for another nonce", resp: func() agents.SwarmProveResp { return good.sign(t, req) }, check: proofReq("m1")},
		{name: "replay for another host", resp: func() agents.SwarmProveResp { return good.sign(t, req) },
			check: agents.SwarmProveReq{Nonce: req.Nonce, ServerID: req.ServerID, HostID: "w1"}},
		{name: "replay for another server", resp: func() agents.SwarmProveResp { return good.sign(t, req) },
			check: agents.SwarmProveReq{Nonce: req.Nonce, ServerID: "server-2", HostID: req.HostID}},
		{name: "no trust root", resp: func() agents.SwarmProveResp { return good.sign(t, req) }, root: "-"},
		{name: "key block in bundle", resp: func() agents.SwarmProveResp {
			p := good.sign(t, req)
			p.CertPEM += string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}}))
			return p
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := req
			if tc.check.HostID != "" {
				check = tc.check
			}
			root := ca.pem
			if tc.root == "-" {
				root = ""
			}
			now := time.Now()
			if !tc.now.IsZero() {
				now = tc.now
			}
			v, err := verifyProof(root, check, tc.resp(), swarmID, "n1", RoleManager, now)
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v, err %v", tc.ok, err)
			}
			if err != nil && !errors.Is(err, ErrProofInvalid) {
				t.Fatalf("error %v does not wrap ErrProofInvalid", err)
			}
			if tc.ok && (!v.notAfter.Equal(later.Truncate(time.Second)) || v.issuer != agents.IssuerKeyID(ca.cert.RawSubjectPublicKeyInfo)) {
				t.Fatalf("verified %+v", v)
			}
		})
	}
}

// provedLab is lab() with a Swarm CA: m1 and m2 hold real manager
// certificates, w1 a real worker certificate.
func provedLab(t *testing.T) (*fakeHosts, *swarmCA) {
	f := lab()
	ca := newSwarmCA(t)
	later := time.Now().Add(90 * 24 * time.Hour)
	f.roots = map[string]string{"m1": ca.pem, "m2": ca.pem}
	f.provers = map[string]func(agents.SwarmProveReq) (agents.SwarmProveResp, error){
		"m1": ca.issue(t, "n1", swarmID, RoleManager, later).prover(t),
		"m2": ca.issue(t, "n2", swarmID, RoleManager, later).prover(t),
		"w1": ca.issue(t, "n3", swarmID, RoleWorker, later).prover(t),
	}
	return f, ca
}

func proofs(c Cluster) map[string]string {
	out := map[string]string{}
	for _, m := range c.Members {
		out[m.HostID] = m.Proof
	}
	return out
}

func TestCreateProvesMembers(t *testing.T) {
	ctx := context.Background()
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"m1": ProofVerified, "m2": ProofVerified, "w1": ProofVerified}
	if got := proofs(c); !equal(got, want) {
		t.Fatalf("proofs %v", got)
	}
	if !c.Trust.Pinned || !c.Trust.OriginVerified || c.Trust.Pending {
		t.Fatalf("trust %+v", c.Trust)
	}
}

func TestCreateRefusesDifferentTrustRoots(t *testing.T) {
	f, _ := provedLab(t)
	f.roots["m2"] = newSwarmCA(t).pem
	s := newService(t, f)
	if _, err := s.Create(context.Background(), "m1", "", "u1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
}

// An agent claiming a node it does not hold never becomes verified, with
// a certificate of its own node or a forged one, and is not asked again
// every reconcile.
func TestFakeAgentClaimingNodeStaysUnverified(t *testing.T) {
	ctx := context.Background()
	f, ca := provedLab(t)
	later := time.Now().Add(90 * 24 * time.Hour)
	// n5 is a manager node without a Dockmesh agent; "fake" claims it.
	nodes := append(f.nodes["m1"], agents.SwarmNode{ID: "n5", Role: RoleManager})
	f.nodes = map[string][]agents.SwarmNode{"m1": nodes, "m2": nodes}
	f.infos["fake"] = swarmInfo(swarmID, "n5", RoleManager)
	own := ca.issue(t, "n4", swarmID, RoleWorker, later)
	forged := newSwarmCA(t).issue(t, "n5", swarmID, RoleManager, later)

	for name, p := range map[string]func(agents.SwarmProveReq) (agents.SwarmProveResp, error){
		"own worker certificate": own.prover(t),
		"forged certificate":     forged.prover(t),
		"refuses": func(agents.SwarmProveReq) (agents.SwarmProveResp, error) {
			return agents.SwarmProveResp{}, host.ErrProofRefused
		},
		"too old": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.provers["fake"] = p
			f.proved = nil
			f.mu.Unlock()
			s := newService(t, f)
			s.SetServerID("server-1")
			if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
				t.Fatal(err)
			}
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			c, _ := s.Get(ctx, swarmID)
			got := proofs(c)
			if got["fake"] == ProofVerified || got["m1"] != ProofVerified || got["m2"] != ProofVerified {
				t.Fatalf("proofs %v", got)
			}
			for _, m := range c.Members {
				if m.HostID == "fake" && m.Verified {
					t.Fatal("fake agent verified")
				}
			}
			if n := f.provedCount("fake"); n != 1 {
				t.Fatalf("fake asked %d times, want 1 within the retry interval", n)
			}
		})
	}
}

// Two reconciles without a change ask nobody again and change no row;
// a new agent session asks that host again.
func TestProofNotRepeatedUntilDue(t *testing.T) {
	ctx := context.Background()
	f, _ := provedLab(t)
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	rows := func() string {
		var out strings.Builder
		r, err := s.db.Query(`SELECT host_id, COALESCE(verified_at, ''), COALESCE(cert_not_after, ''), COALESCE(cert_issuer, '') FROM cluster_memberships ORDER BY host_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var a, b, c, d string
			_ = r.Scan(&a, &b, &c, &d)
			out.WriteString(a + b + c + d + "\n")
		}
		return out.String()
	}
	before := rows()
	for range 2 {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if after := rows(); after != before {
		t.Fatalf("rows changed:\n%s\n%s", before, after)
	}
	if f.provedCount("m2") != 1 {
		t.Fatalf("m2 asked %d times", f.provedCount("m2"))
	}
	f.mu.Lock()
	f.joined = map[string]time.Time{"m2": time.Now().Add(time.Second)}
	f.mu.Unlock()
	s.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.provedCount("m2") != 2 || f.provedCount("m1") != 1 {
		t.Fatalf("after reconnect m1 %d, m2 %d", f.provedCount("m1"), f.provedCount("m2"))
	}
}

// A cluster created before proofs existed has no pinned root; the next
// reconcile pins the origin's when no other manager disagrees.
func TestUpgradePinsTrustRoot(t *testing.T) {
	ctx := context.Background()
	for _, disagree := range []bool{false, true} {
		f, _ := provedLab(t)
		roots := f.roots
		f.roots = nil
		s := newService(t, f)
		s.SetServerID("server-1")
		c, err := s.Create(ctx, "m1", "", "u1")
		if err != nil {
			t.Fatal(err)
		}
		if c.Trust.Pinned || proofs(c)["m2"] != ProofNoTrustRoot {
			t.Fatalf("old agents: trust %+v proofs %v", c.Trust, proofs(c))
		}
		f.mu.Lock()
		f.roots = roots
		if disagree {
			f.roots = map[string]string{"m1": roots["m1"], "m2": newSwarmCA(t).pem}
		}
		f.mu.Unlock()
		s.now = func() time.Time { return time.Now().Add(proofRetry) }
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		c, _ = s.Get(ctx, swarmID)
		if c.Trust.Pinned == disagree {
			t.Fatalf("disagree=%v: pinned %v", disagree, c.Trust.Pinned)
		}
		if !disagree && proofs(c)["m2"] != ProofVerified {
			t.Fatalf("after pin: %v", proofs(c))
		}
		rec := &auditRec{}
		s.SetAudit(rec)
		var before, after string
		_ = s.db.QueryRow(`SELECT COALESCE(trust_root, '') FROM clusters`).Scan(&before)
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		_ = s.db.QueryRow(`SELECT COALESCE(trust_root, '') FROM clusters`).Scan(&after)
		if before != after || len(rec.got) != 0 {
			t.Fatalf("second start changed the root or audited %v", rec.got)
		}
	}
}

func TestTrustRootRotation(t *testing.T) {
	ctx := context.Background()
	later := time.Now().Add(90 * 24 * time.Hour)

	t.Run("two verified managers agree", func(t *testing.T) {
		f, _ := provedLab(t)
		s := newService(t, f)
		s.SetServerID("server-1")
		if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
			t.Fatal(err)
		}
		next := newSwarmCA(t)
		f.mu.Lock()
		f.roots = map[string]string{"m1": next.pem}
		f.provers["m1"] = next.issue(t, "n1", swarmID, RoleManager, later).prover(t)
		f.provers["m2"] = next.issue(t, "n2", swarmID, RoleManager, later).prover(t)
		f.mu.Unlock()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		c, _ := s.Get(ctx, swarmID)
		if !c.Trust.Pending || c.Trust.Confirmable {
			t.Fatalf("one manager reports the new root: %+v", c.Trust)
		}
		if _, err := s.ConfirmTrustRoot(ctx, swarmID, "x", "u1"); !errors.Is(err, ErrConfirmNeedsManagers) {
			t.Fatalf("confirm with two managers: %v", err)
		}
		f.mu.Lock()
		f.roots["m2"] = next.pem
		f.mu.Unlock()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		c, _ = s.Get(ctx, swarmID)
		if c.Trust.Pending || c.trustRoot != canonicalRoot(next.pem) {
			t.Fatalf("rotation not accepted: %+v", c.Trust)
		}
		if got := proofs(c); got["m1"] != ProofVerified || got["m2"] != ProofVerified {
			t.Fatalf("after rotation %v", got)
		}
	})

	t.Run("single manager waits for an admin", func(t *testing.T) {
		f, _ := provedLab(t)
		single := []agents.SwarmNode{{ID: "n1", Role: RoleManager, Leader: true}, {ID: "n3", Role: RoleWorker}}
		f.nodes = map[string][]agents.SwarmNode{"m1": single}
		delete(f.infos, "m2")
		s := newService(t, f)
		s.SetServerID("server-1")
		n := &noticeRec{}
		s.SetNotices(n)
		if _, err := s.ConfirmTrustRoot(ctx, "missing", "x", "u1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown cluster: %v", err)
		}
		if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ConfirmTrustRoot(ctx, swarmID, "x", "u1"); !errors.Is(err, ErrNoPendingRoot) {
			t.Fatalf("nothing pending: %v", err)
		}
		next := newSwarmCA(t)
		f.mu.Lock()
		f.roots = map[string]string{"m1": next.pem}
		f.mu.Unlock()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		c, _ := s.Get(ctx, swarmID)
		if !c.Trust.Pending || !c.Trust.Confirmable || !n.open(trustRootKey(swarmID)) {
			t.Fatalf("trust %+v notices %v", c.Trust, n.active)
		}
		if _, err := s.ConfirmTrustRoot(ctx, swarmID, rootFingerprint(newSwarmCA(t).pem), "u1"); !errors.Is(err, ErrRootChanged) {
			t.Fatalf("confirm another root: %v", err)
		}
		if c.Trust.PendingFingerprint != rootFingerprint(next.pem) || c.Trust.PinnedFingerprint == "" {
			t.Fatalf("fingerprints %+v", c.Trust)
		}
		if c, err := s.ConfirmTrustRoot(ctx, swarmID, strings.ToLower(c.Trust.PendingFingerprint), "u1"); err != nil || c.Trust.Pending || c.trustRoot != canonicalRoot(next.pem) {
			t.Fatalf("confirm: %+v %v", c.Trust, err)
		}
		if n.open(trustRootKey(swarmID)) {
			t.Fatal("notice still open")
		}
	})
}

func TestNoVerifiedManagerNotice(t *testing.T) {
	ctx := context.Background()
	f := lab()
	s := newService(t, f)
	n := &noticeRec{}
	s.SetNotices(n)
	if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !n.open(noVerifiedManagerKey(swarmID)) {
		t.Fatalf("notices %v", n.active)
	}
	if _, _, err := s.Delete(ctx, swarmID, nil); err != nil {
		t.Fatal(err)
	}
	if n.open(noVerifiedManagerKey(swarmID)) {
		t.Fatal("notice outlived the cluster")
	}
}

func TestAddressWarningOnly(t *testing.T) {
	ctx := context.Background()
	f, _ := provedLab(t)
	nodes := f.nodes["m1"]
	nodes[1].Addr = "10.0.0.2"
	f.peers = map[string]string{"m2": "192.0.2.9"}
	s := newService(t, f)
	s.SetServerID("server-1")
	if _, err := s.Create(ctx, "m1", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get(ctx, swarmID)
	for _, m := range c.Members {
		if m.HostID == "m2" && (m.AddrWarning != "192.0.2.9|10.0.0.2" || !m.Verified) {
			t.Fatalf("m2 %+v", m)
		}
	}
}

type noticeRec struct {
	active map[string]bool
}

func (n *noticeRec) Set(_ context.Context, active bool, in notices.RaiseInput) error {
	if n.active == nil {
		n.active = map[string]bool{}
	}
	n.active[in.Key] = active
	return nil
}

func (n *noticeRec) open(key string) bool { return n.active[key] }

// With several managers the origin alone can't pin a root: a manager whose
// agent reports none (too old) is no agreement, and the cluster stays
// unpinned until one does.
func TestPinNeedsSecondManager(t *testing.T) {
	ctx := context.Background()
	f, ca := provedLab(t)
	f.roots = map[string]string{"m1": ca.pem}
	s := newService(t, f)
	s.SetServerID("server-1")
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Trust.Pinned || c.Trust.OriginVerified {
		t.Fatalf("origin alone pinned: %+v", c.Trust)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Get(ctx, swarmID); c.Trust.Pinned {
		t.Fatal("pinned at reconcile without a second manager")
	}
	f.mu.Lock()
	f.roots["m2"] = ca.pem
	f.mu.Unlock()
	s.now = func() time.Time { return time.Now().Add(proofRetry) }
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Get(ctx, swarmID); !c.Trust.Pinned || !c.Trust.OriginVerified {
		t.Fatalf("after m2 agrees: %+v", c.Trust)
	}
}

// A proof that isn't renewed stops counting even while its certificate is
// valid.
func TestStaleProofExpires(t *testing.T) {
	now := time.Now()
	old, recent, notAfter := now.Add(-proofStale-time.Minute), now.Add(-time.Hour), now.Add(30*24*time.Hour)
	if verifiedNow(Member{VerifiedAt: &old, CertNotAfter: &notAfter}, now) {
		t.Fatal("stale proof counts")
	}
	if !verifiedNow(Member{VerifiedAt: &recent, CertNotAfter: &notAfter}, now) {
		t.Fatal("recent proof does not count")
	}
	if verifiedNow(Member{VerifiedAt: &recent, CertNotAfter: &recent}, now) {
		t.Fatal("expired certificate counts")
	}
}

// An origin that leaves another manager out of its node list can't pass as
// the only manager: nothing is pinned and a changed root is not
// admin-confirmable.
func TestOriginHidingManagerPinsNothing(t *testing.T) {
	ctx := context.Background()
	f, ca := provedLab(t)
	forged := newSwarmCA(t)
	hidden := []agents.SwarmNode{{ID: "n1", Role: RoleManager, Leader: true}, {ID: "n3", Role: RoleWorker}}
	f.nodes = map[string][]agents.SwarmNode{"m1": hidden, "m2": f.nodes["m2"]}
	f.roots = map[string]string{"m1": forged.pem, "m2": ca.pem}
	s := newService(t, f)
	s.SetServerID("server-1")
	c, err := s.Create(ctx, "m1", "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Get(ctx, swarmID); c.Trust.Pinned || c.Trust.Confirmable {
		t.Fatalf("trust %+v", c.Trust)
	}
}

// A root bundle with a second CA is no root: the fingerprint of its first
// certificate would not cover the second.
func TestRootBundleRefused(t *testing.T) {
	a, b := newSwarmCA(t), newSwarmCA(t)
	if canonicalRoot(a.pem) == "" {
		t.Fatal("single root refused")
	}
	if canonicalRoot(a.pem+b.pem) != "" {
		t.Fatal("two-certificate root accepted")
	}
	leaf := a.issue(t, "n1", swarmID, RoleManager, time.Now().Add(time.Hour))
	if canonicalRoot(leaf.pem) != "" {
		t.Fatal("non-CA root accepted")
	}
}

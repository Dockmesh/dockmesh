package clusters

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// ErrProofInvalid wraps every reason a node proof is refused.
var ErrProofInvalid = errors.New("swarm node proof refused")

// maxProofCerts bounds the certificates of one proof bundle.
const maxProofCerts = 4

// verified is what a valid proof tells about the node's certificate.
type verified struct {
	notAfter time.Time
	issuer   string
}

// swarmOU is the certificate OU dockerd gives a node of role.
func swarmOU(role string) string {
	if role == RoleManager {
		return "swarm-manager"
	}
	return "swarm-worker"
}

// canonicalRoot returns the root CA certificate of a trust root PEM in one
// canonical encoding, or "" unless it holds exactly one CA certificate: a
// Swarm reports a single root, and a fingerprint of the first certificate
// must cover everything that is trusted.
func canonicalRoot(p string) string {
	if p == "" || len(p) > agents.MaxTrustRootPEM {
		return ""
	}
	certs, err := parseCerts([]byte(p))
	if err != nil || len(certs) != 1 {
		return ""
	}
	var b bytes.Buffer
	for _, c := range certs {
		if !c.IsCA {
			return ""
		}
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return b.String()
}

// rootFingerprint is the SHA-256 of the first certificate of a root PEM,
// as colon-separated upper-case hex, or "".
func rootFingerprint(p string) string {
	certs, err := parseCerts([]byte(p))
	if err != nil || len(certs) == 0 {
		return ""
	}
	sum := sha256.Sum256(certs[0].Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func parseCerts(p []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, p = pem.Decode(p)
		if blk == nil {
			return out, nil
		}
		if blk.Type != "CERTIFICATE" {
			return nil, errors.New("unexpected PEM block " + blk.Type)
		}
		if len(out) == maxProofCerts {
			return nil, errors.New("too many certificates")
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

// verifyProof checks a proof answer against the pinned trust root: the
// bundle's leaf chains to the root and is valid at now, names nodeID
// (CN), clusterID (O) and the role's OU, and its key signed the message
// of req.
func verifyProof(trustRoot string, req agents.SwarmProveReq, p agents.SwarmProveResp, clusterID, nodeID, role string, now time.Time) (verified, error) {
	fail := func(format string, args ...any) (verified, error) {
		return verified{}, fmt.Errorf("%w: %s", ErrProofInvalid, fmt.Sprintf(format, args...))
	}
	roots, err := parseCerts([]byte(trustRoot))
	if err != nil || len(roots) == 0 {
		return fail("no trust root pinned")
	}
	certs, err := parseCerts([]byte(p.CertPEM))
	if err != nil || len(certs) == 0 {
		return fail("certificate bundle unreadable")
	}
	leaf := certs[0]
	rootPool, inter := x509.NewCertPool(), x509.NewCertPool()
	for _, c := range roots {
		rootPool.AddCert(c)
	}
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots: rootPool, Intermediates: inter, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return verified{}, fmt.Errorf("%w: %w", ErrProofInvalid, err)
	}
	subj := leaf.Subject
	if subj.CommonName != nodeID {
		return fail("certificate names node %q, not %q", subj.CommonName, nodeID)
	}
	if !slices.Equal(subj.Organization, []string{clusterID}) {
		return fail("certificate is for another Swarm")
	}
	if !slices.Equal(subj.OrganizationalUnit, []string{swarmOU(role)}) {
		return fail("certificate role %v does not match %s", subj.OrganizationalUnit, role)
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fail("certificate key is not ECDSA")
	}
	msg, err := agents.ProofMessage(req)
	if err != nil {
		return fail("bad request")
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(pub, digest[:], p.Signature) {
		return fail("signature does not match the certificate")
	}
	out := verified{notAfter: leaf.NotAfter}
	if c := chains[0]; len(c) > 1 {
		out.issuer = agents.IssuerKeyID(c[1].RawSubjectPublicKeyInfo)
	}
	return out, nil
}

// unknownAuthority reports whether err is a chain to a root other than the
// pinned one.
func unknownAuthority(err error) bool {
	var ua x509.UnknownAuthorityError
	return errors.As(err, &ua)
}

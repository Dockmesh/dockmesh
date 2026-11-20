package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
)

// ProofNonceSize is the length of the nonce a SwarmProveReq carries.
const ProofNonceSize = 32

// maxProofCertPEM bounds the certificate bundle the server accepts; a node
// bundle is the leaf plus at most an intermediate during a CA rotation.
const maxProofCertPEM = 16 << 10

// MaxTrustRootPEM bounds the trust root the server accepts from a manager.
const MaxTrustRootPEM = 16 << 10

// SwarmProveReq asks an agent to show that its daemon holds a Swarm node
// key. ServerID and HostID are part of the signed message, so a proof is
// bound to one server and one host besides the nonce.
type SwarmProveReq struct {
	Nonce    []byte `json:"nonce"`
	ServerID string `json:"server_id"`
	HostID   string `json:"host_id"`
}

// SwarmProveResp is the agent's answer: the node certificate bundle
// (PEM, CERTIFICATE blocks only) and an ASN.1 ECDSA signature over
// ProofMessage made with the node key.
type SwarmProveResp struct {
	CertPEM   string `json:"cert_pem"`
	Signature []byte `json:"signature"`
}

var ErrBadProofRequest = errors.New("invalid swarm proof request")

// ProofMessage is what the node key signs. The nonce has a fixed size and
// the ids can't contain NUL, so the encoding is unambiguous.
func ProofMessage(req SwarmProveReq) ([]byte, error) {
	if len(req.Nonce) != ProofNonceSize || !proofID(req.ServerID) || !proofID(req.HostID) {
		return nil, ErrBadProofRequest
	}
	msg := make([]byte, 0, 32+len(req.Nonce)+len(req.ServerID)+len(req.HostID))
	msg = append(msg, "dockmesh-swarm-proof\x00"...)
	msg = append(msg, req.Nonce...)
	msg = append(msg, req.ServerID...)
	msg = append(msg, 0)
	msg = append(msg, req.HostID...)
	return msg, nil
}

func proofID(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsRune(s, 0)
}

// CertificatesOnly re-encodes the CERTIFICATE blocks of a PEM bundle and
// drops everything else, so no other block (a key above all) can travel
// in a proof. It returns "" when the bundle has no certificate or is too
// large.
func CertificatesOnly(bundle []byte) string {
	var b strings.Builder
	for {
		var blk *pem.Block
		blk, bundle = pem.Decode(bundle)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		b.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: blk.Bytes}))
		if b.Len() > maxProofCertPEM {
			return ""
		}
	}
	return b.String()
}

// SanitizeProof bounds a proof answer from an agent.
func SanitizeProof(p SwarmProveResp) (SwarmProveResp, bool) {
	if len(p.CertPEM) > maxProofCertPEM || len(p.Signature) == 0 || len(p.Signature) > 256 {
		return SwarmProveResp{}, false
	}
	return p, true
}

// IssuerKeyID identifies a certificate issuer by its public key, the form
// a Swarm node list reports (TLSInfo.CertIssuerPublicKey).
func IssuerKeyID(rawPublicKey []byte) string {
	if len(rawPublicKey) == 0 {
		return ""
	}
	sum := sha256.Sum256(rawPublicKey)
	return hex.EncodeToString(sum[:])
}

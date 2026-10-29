// Package pki manages the agent mTLS PKI: a self-signed CA that signs both
// the central server's TLS cert (used by the agent listener) and every
// enrolled agent's client cert. Concept §15.3.
//
// Files persisted under data/:
//   agents-ca.crt          public CA cert  — bundled into agent at enrollment
//   agents-ca.key          CA private key  — root of trust, 0400
//   agents-server.crt      server cert for the mTLS listener
//   agents-server.key      server private key, 0400
//
// The package only uses crypto/x509, no external dependencies.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Manager owns the CA + server cert and issues client certs for agents.
type Manager struct {
	dir string

	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	caCertPEM  []byte
	serverCert []byte // PEM
	serverKey  []byte // PEM
}

// New loads or creates the CA and the server cert. dir is the directory
// where the cert/key files live (typically ./data). hosts is the list of
// SANs the server cert should cover (hostnames + IPs the agents will dial).
func New(dir string, hosts []string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir}
	if err := m.ensureCA(); err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	if err := m.ensureServerCert(hosts); err != nil {
		return nil, fmt.Errorf("server cert: %w", err)
	}
	return m, nil
}

// CACertPEM returns the PEM-encoded CA cert. Bundled into agents on enroll
// so they can verify the server.
func (m *Manager) CACertPEM() []byte { return m.caCertPEM }

// CAFingerprint returns the SHA-256 (hex) of the CA cert's DER encoding.
// The agent installer pins it so enrollment only talks to this server.
func (m *Manager) CAFingerprint() string { return certFingerprint(m.caCert.Raw) }

// ServerCertPEM / ServerKeyPEM return the listener's identity. Used to
// build a tls.Config in the caller (so we don't import net/http here).
func (m *Manager) ServerCertPEM() []byte { return m.serverCert }
func (m *Manager) ServerKeyPEM() []byte  { return m.serverKey }

// CACertPool returns an x509.CertPool with just the CA cert in it. Use
// this as ClientCAs / RootCAs in tls.Config.
func (m *Manager) CACertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.caCertPEM)
	return pool
}

// IssueClientCert mints a new client cert signed by the CA. CommonName is
// the agent's UUID. validity is how long the cert is good for. Returns
// PEM-encoded cert and private key.
func (m *Manager) IssueClientCert(commonName string, validity time.Duration) (certPEM, keyPEM []byte, fingerprint string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	certPEM, fingerprint, _, err = m.signClient(&key.PublicKey, commonName, validity)
	if err != nil {
		return nil, nil, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, "", err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, fingerprint, nil
}

// SignClientCSR issues a client cert for the key in a PEM CSR, so the
// private key never leaves the agent. Only the key is taken from the
// CSR; the subject is always commonName. Keys must be ECDSA P-256.
func (m *Manager) SignClientCSR(csrPEM []byte, commonName string, validity time.Duration) (certPEM []byte, fingerprint string, notAfter time.Time, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", time.Time{}, errors.New("csr: no CERTIFICATE REQUEST PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("csr signature: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, "", time.Time{}, errors.New("csr: key must be ECDSA P-256")
	}
	return m.signClient(pub, commonName, validity)
}

func (m *Manager) signClient(pub *ecdsa.PublicKey, commonName string, validity time.Duration) ([]byte, string, time.Time, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Dockmesh Agent"}},
		NotBefore:    now.Add(-1 * time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.caCert, pub, m.caKey)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, certFingerprint(der), template.NotAfter, nil
}

// FingerprintFromCert returns the SHA-256 of the DER-encoded cert.
func FingerprintFromCert(cert *x509.Certificate) string {
	return certFingerprint(cert.Raw)
}

func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// -----------------------------------------------------------------------------
// CA management
// -----------------------------------------------------------------------------

func (m *Manager) ensureCA() error {
	certPath := filepath.Join(m.dir, "agents-ca.crt")
	keyPath := filepath.Join(m.dir, "agents-ca.key")

	certBytes, certErr := os.ReadFile(certPath)
	keyBytes, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		cert, err := parseCertPEM(certBytes)
		if err != nil {
			return err
		}
		key, err := parseECKeyPEM(keyBytes)
		if err != nil {
			return err
		}
		m.caCert = cert
		m.caKey = key
		m.caCertPEM = certBytes
		return nil
	}
	if certErr != nil && !errors.Is(certErr, os.ErrNotExist) {
		return certErr
	}
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return keyErr
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Dockmesh Agent CA",
			Organization: []string{"Dockmesh"},
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := WriteFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := WriteFileAtomic(keyPath, keyPEM, 0o400); err != nil {
		return err
	}
	m.caCert = cert
	m.caKey = key
	m.caCertPEM = certPEM
	return nil
}

// -----------------------------------------------------------------------------
// Server cert (for the :8443 mTLS listener)
// -----------------------------------------------------------------------------

func (m *Manager) ensureServerCert(hosts []string) error {
	certPath := filepath.Join(m.dir, "agents-server.crt")
	keyPath := filepath.Join(m.dir, "agents-server.key")

	certBytes, cErr := os.ReadFile(certPath)
	keyBytes, kErr := os.ReadFile(keyPath)
	if cErr == nil && kErr == nil {
		// Reuse if it covers all the requested hosts; otherwise re-issue.
		cert, err := parseCertPEM(certBytes)
		if err == nil && coversHosts(cert, hosts) && keyMatches(certBytes, keyBytes) {
			m.serverCert = certBytes
			m.serverKey = keyBytes
			return nil
		}
	} else {
		if cErr != nil && !errors.Is(cErr, os.ErrNotExist) {
			return cErr
		}
		if kErr != nil && !errors.Is(kErr, os.ErrNotExist) {
			return kErr
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "dockmesh-agent-server", Organization: []string{"Dockmesh"}},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	// Always include localhost / 127.0.0.1 / ::1 so on-host operators can
	// dial the server even when their public DNS doesn't resolve.
	template.DNSNames = append(template.DNSNames, "localhost")
	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))

	der, err := x509.CreateCertificate(rand.Reader, template, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Key first: a crash between the two renames leaves a new key with
	// the old cert, which the next start detects and re-issues.
	if err := WriteFileAtomic(keyPath, keyPEM, 0o400); err != nil {
		return err
	}
	if err := WriteFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	m.serverCert = certPEM
	m.serverKey = keyPEM
	return nil
}

// keyMatches reports whether the key PEM belongs to the cert PEM.
func keyMatches(certPEM, keyPEM []byte) bool {
	_, err := tls.X509KeyPair(certPEM, keyPEM)
	return err == nil
}

// WriteFileAtomic replaces path with data via a temp file in the same
// directory and a rename, so readers never see a half-written file and a
// read-only (0400) target can be replaced without root.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func coversHosts(cert *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			found := false
			for _, c := range cert.IPAddresses {
				if c.Equal(ip) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
			continue
		}
		found := false
		for _, d := range cert.DNSNames {
			if d == h {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// PEM helpers
// -----------------------------------------------------------------------------

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

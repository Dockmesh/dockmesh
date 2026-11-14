package pki

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Files of the web UI certificate, next to the CA. It is issued once and
// kept, so the fingerprint the installer printed stays valid.
const (
	UICertFile = "ui-server.crt"
	uiKeyFile  = "ui-server.key"
)

// maxCertFile bounds what UIFingerprint reads.
const maxCertFile = 64 << 10

// EnsureUICert loads the web UI certificate, or issues one signed by the CA
// for hosts plus the loopback names when there is none or it is unusable.
// Hosts missing from an existing certificate do not re-issue it: the
// browser does not trust the CA anyway, and the fingerprint is what the
// operator checks.
func (m *Manager) EnsureUICert(hosts []string) (*tls.Certificate, error) {
	certPath := filepath.Join(m.dir, UICertFile)
	keyPath := filepath.Join(m.dir, uiKeyFile)
	certPEM, cErr := os.ReadFile(certPath)
	keyPEM, kErr := os.ReadFile(keyPath)
	if cErr == nil && kErr == nil {
		if cert, err := parseCertPEM(certPEM); err == nil && m.pairProblem(cert, keyPEM) == "" {
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err == nil {
				pair.Leaf = cert
				return &pair, nil
			}
		}
	} else if err := errors.Join(cErr, kErr); !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var dns []string
	var ips []net.IP
	for _, h := range hosts {
		h = strings.Trim(strings.TrimSpace(h), "[]")
		switch ip := net.ParseIP(h); {
		case ip != nil:
			ips = append(ips, ip)
		case h != "" && validDNSName(h):
			dns = append(dns, h)
		}
	}
	if len(dns)+len(ips) > maxServerSANs {
		return nil, fmt.Errorf("the web certificate would carry %d names and IPs, more than %d", len(dns)+len(ips), maxServerSANs)
	}
	cert, newCert, newKey, err := m.issueLeaf("dockmesh-web", dns, ips, nil)
	if err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(keyPath, newKey, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", uiKeyFile, err)
	}
	if err := WriteFileAtomic(certPath, newCert, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", UICertFile, err)
	}
	pair, err := tls.X509KeyPair(newCert, newKey)
	if err != nil {
		return nil, err
	}
	pair.Leaf = cert
	return &pair, nil
}

func validDNSName(h string) bool {
	for _, l := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if !validLabel(l) {
			return false
		}
	}
	return true
}

// Fingerprint formats the SHA-256 of a DER certificate the way browsers
// show it: upper-case hex pairs separated by colons.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(sum))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// UIFingerprint reads the web UI certificate in dir and returns its
// Fingerprint. A symlink or anything but a small regular file is refused:
// root reads this file from a directory the service owns.
func UIFingerprint(dir string) (string, error) {
	cert, err := ReadUICert(dir)
	if err != nil {
		return "", err
	}
	return Fingerprint(cert.Raw), nil
}

// ReadUICert parses the web UI certificate in dir with the checks of
// UIFingerprint.
func ReadUICert(dir string) (*x509.Certificate, error) {
	path := filepath.Join(dir, UICertFile)
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0) // #nosec G304 -- fixed name below the data dir
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxCertFile {
		return nil, fmt.Errorf("%s is not a certificate file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCertFile))
	if err != nil {
		return nil, err
	}
	return parseCertPEM(data)
}

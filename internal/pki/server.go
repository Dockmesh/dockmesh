package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const serverCertValidity = 5 * 365 * 24 * time.Hour

// ensureServerCert loads the listener cert from disk and only issues a new
// one when the file is missing, unusable (key mismatch, foreign CA) or does
// not cover every entry in hosts. A re-issued cert keeps the SANs of the
// old one, so names added with `dockmesh ca reissue-server` survive a
// restart with an unchanged DOCKMESH_AGENT_SANS.
func (m *Manager) ensureServerCert(hosts []string) error {
	certPath := filepath.Join(m.dir, serverCertFile)
	keyPath := filepath.Join(m.dir, serverKeyFile)

	certPEM, cErr := os.ReadFile(certPath)
	if cErr != nil && !errors.Is(cErr, os.ErrNotExist) {
		return cErr
	}
	keyPEM, kErr := os.ReadFile(keyPath)
	if kErr != nil && !errors.Is(kErr, os.ErrNotExist) {
		return kErr
	}

	var old *x509.Certificate
	if cErr == nil {
		var err error
		if old, err = parseCertPEM(certPEM); err != nil {
			slog.Warn("agent server certificate does not parse, issuing a new one", "path", certPath, "err", err)
		}
	}
	if old != nil && kErr == nil {
		problem := m.pairProblem(old, keyPEM)
		if problem == "" && coversHosts(old, hosts) {
			m.setServer(old, certPEM, keyPEM)
			return nil
		}
		if problem != "" {
			slog.Warn("agent server certificate unusable, issuing a new one", "reason", problem)
		} else {
			slog.Info("agent server certificate lacks configured SANs, issuing a new one", "hosts", hosts)
		}
	}

	var dns []string
	var ips []net.IP
	if old != nil {
		dns = append(dns, old.DNSNames...)
		ips = append(ips, old.IPAddresses...)
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	cert, newCert, newKey, err := m.issueServerCert(dns, ips)
	if err != nil {
		return err
	}
	if err := writeServerPair(m.dir, newCert, newKey); err != nil {
		return err
	}
	m.setServer(cert, newCert, newKey)
	return nil
}

func (m *Manager) setServer(cert *x509.Certificate, certPEM, keyPEM []byte) {
	m.serverX509 = cert
	m.serverCert = certPEM
	m.serverKey = keyPEM
}

// pairProblem says why cert + keyPEM cannot serve the listener, or "" if
// they can. tls.X509KeyPair would only fail later, after startup, with the
// listener silently disabled.
func (m *Manager) pairProblem(cert *x509.Certificate, keyPEM []byte) string {
	key, err := parseECKeyPEM(keyPEM)
	if err != nil {
		return "private key does not parse: " + err.Error()
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return "private key does not match certificate"
	}
	if err := cert.CheckSignatureFrom(m.caCert); err != nil {
		return "certificate is not signed by the agent CA"
	}
	return ""
}

// issueServerCert signs a fresh key with the CA for the given SANs plus
// the loopback names, deduplicated.
func (m *Manager) issueServerCert(dns []string, ips []net.IP) (*x509.Certificate, []byte, []byte, error) {
	// Loopback is always included so on-host operators can dial the
	// listener even when the public name does not resolve locally.
	dns, ips = mergeSANs(append([]string{"localhost"}, dns...),
		append([]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, ips...))

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "dockmesh-agent-server", Organization: []string{"Dockmesh"}},
		NotBefore:    now.Add(-1 * time.Minute),
		NotAfter:     now.Add(serverCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return cert, certPEM, keyPEM, nil
}

// writeServerPair replaces each file via temp file + rename, so a restored
// read-only (0400) key cannot block the rewrite halfway and leave the new
// cert next to the old key. The key goes first: a cert is never on disk
// without its key.
func writeServerPair(dir string, certPEM, keyPEM []byte) error {
	if err := writeFileAtomic(filepath.Join(dir, serverKeyFile), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", serverKeyFile, err)
	}
	if err := writeFileAtomic(filepath.Join(dir, serverCertFile), certPEM, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", serverCertFile, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	ownerRef := path
	if _, err := os.Stat(path); err != nil {
		ownerRef = dir
	}
	if err := chownLike(tmp, ownerRef); err != nil {
		_ = tmp.Close()
		return err
	}
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
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows maps 0400 to the read-only attribute and refuses to
		// rename over a read-only file.
		_ = os.Chmod(path, 0o600)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// copyFileExcl copies src to a new file dst; it fails if dst exists.
func copyFileExcl(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := chownLike(out, src); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ReissueResult describes what ReissueServerCert did.
type ReissueResult struct {
	Added      []string // requested SANs the old cert lacked
	Present    []string // requested SANs the old cert already had
	OldSANs    []string
	SANs       []string // SANs of the cert now on disk
	BackupCert string   // empty when nothing was rewritten
	BackupKey  string   // empty when nothing was rewritten or there was no key
	NotAfter   time.Time
}

// Changed reports whether a new certificate was written.
func (r *ReissueResult) Changed() bool { return r.BackupCert != "" }

// ReissueServerCert replaces the agent listener cert in dir with one signed
// by the existing CA, covering the old cert's SANs plus add. The old files
// are copied to <name>.bak-<timestamp> first. Nothing is written when add
// is already covered and the current cert/key pair is valid.
func ReissueServerCert(dir string, add []string, now time.Time) (*ReissueResult, error) {
	if len(add) == 0 {
		return nil, errors.New("at least one SAN to add is required")
	}
	var addDNS []string
	var addIPs []net.IP
	var errs []error
	for _, s := range add {
		ip, name, err := parseSAN(s)
		switch {
		case err != nil:
			errs = append(errs, err)
		case ip != nil:
			addIPs = append(addIPs, ip)
		default:
			addDNS = append(addDNS, name)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	m := &Manager{dir: dir}
	if err := m.loadCA(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("agent CA not found in %s; restore the system backup first: %w", dir, err)
		}
		return nil, fmt.Errorf("load agent CA: %w", err)
	}
	certPath := filepath.Join(dir, serverCertFile)
	keyPath := filepath.Join(dir, serverKeyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read current server cert: %w", err)
	}
	old, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read current server key: %w", err)
	}
	haveKey := err == nil

	res := &ReissueResult{OldSANs: certSANs(old)}
	addDNS, addIPs = mergeSANs(addDNS, addIPs)
	for _, d := range addDNS {
		if hasDNS(old.DNSNames, d) {
			res.Present = append(res.Present, d)
		} else {
			res.Added = append(res.Added, d)
		}
	}
	for _, ip := range addIPs {
		if hasIP(old.IPAddresses, ip) {
			res.Present = append(res.Present, ip.String())
		} else {
			res.Added = append(res.Added, ip.String())
		}
	}
	if len(res.Added) == 0 && haveKey && m.pairProblem(old, keyPEM) == "" {
		res.SANs = res.OldSANs
		res.NotAfter = old.NotAfter
		return res, nil
	}

	cert, newCert, newKey, err := m.issueServerCert(
		append(append([]string{}, old.DNSNames...), addDNS...),
		append(append([]net.IP{}, old.IPAddresses...), addIPs...))
	if err != nil {
		return nil, fmt.Errorf("issue server cert: %w", err)
	}

	suffix := ".bak-" + now.UTC().Format("20060102T150405Z")
	if err := copyFileExcl(certPath, certPath+suffix, 0o644); err != nil {
		return nil, fmt.Errorf("back up server cert: %w", err)
	}
	res.BackupCert = certPath + suffix
	if haveKey {
		if err := copyFileExcl(keyPath, keyPath+suffix, 0o600); err != nil {
			return nil, fmt.Errorf("back up server key: %w", err)
		}
		res.BackupKey = keyPath + suffix
	}
	if err := writeServerPair(dir, newCert, newKey); err != nil {
		return nil, err
	}
	res.SANs = certSANs(cert)
	res.NotAfter = cert.NotAfter
	return res, nil
}

// parseSAN validates one --add-san value. It returns the IP for IP
// literals (brackets allowed for IPv6) or the lower-cased DNS name.
func parseSAN(s string) (net.IP, string, error) {
	raw := s
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", errors.New("empty SAN")
	}
	if ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		return ip, "", nil
	}
	if strings.Contains(s, "://") || strings.ContainsAny(s, "/:@?#[] \t") {
		return nil, "", fmt.Errorf("%q: want a bare hostname or IP address, not a URL or host:port", raw)
	}
	name := strings.ToLower(strings.TrimSuffix(s, "."))
	if len(name) > 253 {
		return nil, "", fmt.Errorf("%q: hostname longer than 253 characters", raw)
	}
	labels := strings.Split(name, ".")
	for i, l := range labels {
		if i == 0 && l == "*" && len(labels) > 2 {
			continue
		}
		if !validLabel(l) {
			return nil, "", fmt.Errorf("%q: invalid hostname label %q", raw, l)
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return nil, "", fmt.Errorf("%q: not a valid IP address or hostname", raw)
	}
	return nil, name, nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// mergeSANs drops duplicate names (case-insensitive) and IPs, keeping the
// first occurrence's order.
func mergeSANs(dns []string, ips []net.IP) ([]string, []net.IP) {
	var outDNS []string
	for _, d := range dns {
		if d != "" && !hasDNS(outDNS, d) {
			outDNS = append(outDNS, d)
		}
	}
	var outIPs []net.IP
	for _, ip := range ips {
		if ip == nil || hasIP(outIPs, ip) {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		outIPs = append(outIPs, ip)
	}
	return outDNS, outIPs
}

func hasDNS(list []string, name string) bool {
	for _, d := range list {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

func hasIP(list []net.IP, ip net.IP) bool {
	for _, c := range list {
		if c.Equal(ip) {
			return true
		}
	}
	return false
}

func coversHosts(cert *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
			if !hasIP(cert.IPAddresses, ip) {
				return false
			}
			continue
		}
		if !hasDNS(cert.DNSNames, h) {
			return false
		}
	}
	return true
}

func certSANs(cert *x509.Certificate) []string {
	out := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

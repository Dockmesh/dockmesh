// Package webtls picks what the web listener serves: plain HTTP, a
// certificate from the internal PKI, or the operator's own certificate
// files, which are reloaded when they change (certbot renewals).
package webtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Mode is DOCKMESH_TLS_MODE.
type Mode string

const (
	// Plain is plain HTTP on DOCKMESH_HTTP_ADDR.
	Plain Mode = ""
	// Internal serves a certificate from the internal PKI.
	Internal Mode = "internal"
	// File serves DOCKMESH_TLS_CERT_FILE and DOCKMESH_TLS_KEY_FILE.
	File Mode = "file"
	// Caddy is plain HTTP on loopback behind the managed Caddy proxy,
	// which holds the public certificate.
	Caddy Mode = "caddy"
	// Proxy is plain HTTP on a chosen address behind the operator's own
	// reverse proxy.
	Proxy Mode = "proxy"
)

// DefaultProxyAddr is where Caddy and Proxy listen without
// DOCKMESH_HTTP_ADDR: loopback, so only a proxy on this host reaches it.
const DefaultProxyAddr = "127.0.0.1:8080"

// ParseMode checks a DOCKMESH_TLS_MODE value.
func ParseMode(v string) (Mode, error) {
	switch m := Mode(v); m {
	case Plain, Internal, File, Caddy, Proxy:
		return m, nil
	}
	return "", fmt.Errorf("DOCKMESH_TLS_MODE must be internal, file, caddy or proxy, got %q", v)
}

// TLS reports whether the listener itself serves HTTPS in mode m.
func (m Mode) TLS() bool { return m == Internal || m == File }

// BehindProxy reports whether a reverse proxy on another address
// forwards to the listener in mode m.
func (m Mode) BehindProxy() bool { return m == Caddy || m == Proxy }

// DefaultTrustedProxies is the trusted proxy list of mode m listening on
// addr when the operator set none: the bind address alone, for a listener
// bound to one address. A wildcard bind gets none: trusting every peer
// would let anyone fake their client address.
func DefaultTrustedProxies(m Mode, addr string) string {
	if !m.BehindProxy() {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return ""
	}
	return ip.String()
}

const maxPEMFile = 1 << 20

// LoadPair reads and parses a certificate and key file, and says what is
// wrong in words an operator can act on.
func LoadPair(certFile, keyFile string) (*tls.Certificate, error) {
	certPEM, err := readPEM(certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := readPEM(keyFile)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("%s and %s are not a matching certificate and key: %w", certFile, keyFile, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", certFile, err)
	}
	pair.Leaf = leaf
	return &pair, nil
}

func readPEM(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("DOCKMESH_TLS_CERT_FILE and DOCKMESH_TLS_KEY_FILE must both be set")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s must be an absolute path", path)
	}
	// Opened without blocking and checked on the open file: a FIFO or
	// device swapped in would block or never end the read, and the root
	// updater reads this path too. Symlinks are followed, as certbot's
	// live/ directory uses them.
	// Stat first so a link to a device is refused before its open runs.
	pre, err := os.Stat(path)
	if err == nil && !pre.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|oNonBlock, 0) // #nosec G304 -- certificate path from the root-owned config
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%s is not readable by the service account: %w", path, err)
		}
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || !os.SameFile(pre, fi) {
		return nil, fmt.Errorf("%s is not a regular file, or changed while it was opened", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPEMFile+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxPEMFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxPEMFile)
	}
	return data, nil
}

// ReadLeaf parses the first certificate of a PEM file, for a client that
// pins what the listener serves.
func ReadLeaf(certFile string) (*x509.Certificate, error) {
	data, err := readPEM(certFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", certFile)
	}
	return x509.ParseCertificate(block.Bytes)
}

// Reloader serves a certificate pair from files and loads it again when
// either file changes. A pair that does not load keeps the previous one in
// service, so a renewal caught halfway does not take the listener down.
type Reloader struct {
	certFile, keyFile string
	interval          time.Duration
	now               func() time.Time

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   [2]fileStamp
	checked time.Time
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// NewReloader loads the pair once and fails when it does not load.
func NewReloader(certFile, keyFile string) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile, interval: 10 * time.Second, now: time.Now}
	cert, err := LoadPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	r.cert, r.stamp, r.checked = cert, r.stamps(), r.now()
	return r, nil
}

func (r *Reloader) stamps() [2]fileStamp {
	var out [2]fileStamp
	for i, p := range []string{r.certFile, r.keyFile} {
		if fi, err := os.Stat(p); err == nil {
			out[i] = fileStamp{mod: fi.ModTime(), size: fi.Size()}
		}
	}
	return out
}

// Certificate returns the current pair, looking at the files at most once
// per interval.
func (r *Reloader) Certificate() *tls.Certificate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.Sub(r.checked) >= r.interval {
		r.checked = now
		if st := r.stamps(); st != r.stamp {
			cert, err := LoadPair(r.certFile, r.keyFile)
			if err == nil && (now.Before(cert.Leaf.NotBefore) || now.After(cert.Leaf.NotAfter)) {
				err = fmt.Errorf("the certificate is valid from %s to %s only", cert.Leaf.NotBefore, cert.Leaf.NotAfter)
			}
			if err != nil {
				slog.Warn("TLS certificate files changed but do not load; still serving the previous certificate", "cert", r.certFile, "err", err)
			} else {
				r.cert, r.stamp = cert, st
				slog.Info("TLS certificate reloaded", "cert", r.certFile, "not_after", cert.Leaf.NotAfter)
			}
		}
	}
	return r.cert
}

// ServerConfig is the listener config serving the pair of r.
func ServerConfig(r *Reloader) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return r.Certificate(), nil },
	}
}

// ListenAddr is the listen address of mode m for DOCKMESH_HTTP_ADDR addr.
func ListenAddr(m Mode, addr string) string {
	switch {
	case addr != "":
		return addr
	case m.BehindProxy():
		return DefaultProxyAddr
	}
	return ":8080"
}

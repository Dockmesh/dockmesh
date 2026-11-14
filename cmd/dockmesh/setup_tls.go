package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// maxSetupCertHosts keeps the setup certificate below the PKI's SAN cap on
// hosts with many interfaces.
const maxSetupCertHosts = 24

// setupCertificate loads or issues the certificate setup mode serves, from
// the PKI in the setup state directory.
func setupCertificate(cfg *config.Config) (*tls.Certificate, error) {
	dir, err := setup.EnsureStateDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	m, err := pki.New(dir, nil)
	if err != nil {
		return nil, err
	}
	return m.EnsureUICert(setupCertHosts(cfg.BaseURL))
}

// setupCertHosts are the names a browser on the LAN may use for this
// host: the configured base URL host, the host name and the global
// addresses of its physical interfaces.
func setupCertHosts(baseURL string) []string {
	var hosts []string
	if baseURL != "" && baseURL != config.DefaultBaseURL {
		if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || virtualInterface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() && !n.IP.IsLinkLocalMulticast() {
				hosts = append(hosts, n.IP.String())
			}
		}
	}
	if len(hosts) > maxSetupCertHosts {
		hosts = hosts[:maxSetupCertHosts]
	}
	return hosts
}

// virtualInterface names container and VM bridges: a browser never uses
// their addresses, and the certificate shows its names to anyone who
// connects.
func virtualInterface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "lxc"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// selfProbe returns the loopback base URL of this server's own web
// listener and a client for it; with leaf set, over HTTPS trusting only
// that certificate.
func selfProbe(httpAddr string, leaf *x509.Certificate) (string, *http.Client) {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		port = "8080"
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	transport := &http.Transport{Proxy: nil}
	scheme := "http://"
	if leaf != nil {
		transport.TLSClientConfig = pinnedTLS(leaf)
		scheme = "https://"
	}
	return scheme + net.JoinHostPort(host, port), &http.Client{Timeout: 3 * time.Second, Transport: transport}
}

// pinnedTLS accepts exactly the certificate leaf, whatever name it is
// reached by: a loopback probe of our own listener has no name to verify.
func pinnedTLS(leaf *x509.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection pins the exact leaf
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, leaf.Raw) {
				return errors.New("the listener does not serve the expected certificate")
			}
			return nil
		},
	}
}

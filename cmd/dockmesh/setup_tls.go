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
// host: the configured base URL host, the host name and every global
// interface address.
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
	if addrs, err := net.InterfaceAddrs(); err == nil {
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

// selfProbe returns the loopback base URL of this server's own web
// listener and a client for it that trusts only the certificate it serves.
func selfProbe(httpAddr string, webTLS *tls.Config) (string, *http.Client) {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		port = "8080"
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if webTLS == nil || len(webTLS.Certificates) == 0 || webTLS.Certificates[0].Leaf == nil {
		return "http://" + net.JoinHostPort(host, port), client
	}
	client.Transport = &http.Transport{Proxy: nil, TLSClientConfig: pinnedTLS(webTLS.Certificates[0].Leaf)}
	return "https://" + net.JoinHostPort(host, port), client
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

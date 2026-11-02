package agents

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Check statuses.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
	CheckSkip = "skip"
)

// ReachabilityCheck is one step of the agent URL preflight.
type ReachabilityCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Reachability is the result of CheckReachability: the URLs in use and
// what this server sees when it dials the agent URL itself.
type Reachability struct {
	Endpoints
	ListenerEnabled bool                `json:"listener_enabled"`
	CertSANs        []string            `json:"cert_sans"`
	ResolvedIPs     []string            `json:"resolved_ips"`
	Checks          []ReachabilityCheck `json:"checks"`
	OK              bool                `json:"ok"`
}

// reachDialTimeout bounds each network step of the preflight.
const reachDialTimeout = 5 * time.Second

// CheckReachability dials the agent URL from this server the way an agent
// would: resolve the name, connect, and complete a TLS handshake that has
// to present this server's own listener certificate. A result from the
// server's point of view cannot prove that remote hosts get the same
// answer, which the messages say where it matters.
func (s *Service) CheckReachability(ctx context.Context) Reachability {
	e := s.Endpoints()
	s.urlMu.RLock()
	listenerEnabled := s.listenerOn
	s.urlMu.RUnlock()
	r := Reachability{Endpoints: e, ListenerEnabled: listenerEnabled}
	if s.pki != nil {
		r.CertSANs = s.pki.ServerCertSANs()
	}
	add := func(id, status, msg string) {
		r.Checks = append(r.Checks, ReachabilityCheck{ID: id, Status: status, Message: msg})
	}
	finish := func() Reachability {
		r.OK = true
		for _, c := range r.Checks {
			if c.Status == CheckFail {
				r.OK = false
			}
		}
		return r
	}

	if !listenerEnabled {
		add("listener", CheckFail, "The agent listener is turned off (DOCKMESH_AGENT_LISTEN is empty), so no agent can connect.")
		return finish()
	}
	u, err := url.Parse(e.AgentURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "wss" && u.Scheme != "ws") {
		add("url", CheckFail, "The agent URL "+e.AgentURL+" is not a wss:// (or ws://) URL with a host.")
		return finish()
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "ws" {
			port = "80"
		}
	}
	if isLoopbackName(host) {
		add("url", CheckFail, "The agent URL points at "+host+", which every remote host resolves to itself. "+
			"Set the agent URL to a name or address the remote hosts can reach.")
		return finish()
	}
	add("url", CheckOK, "Agents connect to "+e.AgentURL+".")

	if s.pki != nil && !s.pki.ServerCertCovers(host) {
		add("cert", CheckFail, "The agent listener certificate does not cover "+host+"; agents would reject it.")
	} else {
		add("cert", CheckOK, "The agent listener certificate covers "+host+".")
	}

	var ips []string
	if ip := net.ParseIP(host); ip != nil {
		ips = []string{ip.String()}
		add("dns", CheckSkip, "The agent URL uses an IP address; no name lookup needed.")
	} else {
		lctx, cancel := context.WithTimeout(ctx, reachDialTimeout)
		addrs, err := s.lookupHost(lctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			add("dns", CheckFail, "This server cannot resolve "+host+". Remote hosts need a DNS record for it, "+
				"or use an IP address in the agent URL.")
			return finish()
		}
		ips = addrs
		add("dns", CheckOK, "This server resolves "+host+" to "+strings.Join(addrs, ", ")+
			". With split DNS remote hosts may get a different address; it must also lead to this server's port "+port+".")
	}
	r.ResolvedIPs = ips

	dctx, cancel := context.WithTimeout(ctx, reachDialTimeout)
	defer cancel()
	conn, err := s.dialContext(dctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		add("connect", CheckWarn, "This server could not open a connection to "+net.JoinHostPort(host, port)+" ("+shortNetErr(err)+"). "+
			"A firewall, missing port forward or a router without hairpin NAT can cause this; remote hosts may still get through, "+
			"but if they cannot, open TCP port "+port+" to this server.")
		return finish()
	}
	add("connect", CheckOK, "TCP port "+port+" on "+host+" accepts connections.")

	if u.Scheme == "ws" {
		_ = conn.Close()
		add("tls", CheckWarn, "The agent URL uses ws://; agents only connect over wss://.")
		return finish()
	}
	tconn := tls.Client(conn, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		// Verified below against the agents CA, so a foreign certificate
		// can be reported instead of a bare handshake error.
		InsecureSkipVerify: true, //nolint:gosec // chain checked against the agents CA right after the handshake
	})
	_ = tconn.SetDeadline(time.Now().Add(reachDialTimeout))
	err = tconn.HandshakeContext(dctx)
	if err != nil {
		_ = tconn.Close()
		add("tls", CheckFail, "The TLS handshake with "+host+":"+port+" failed ("+shortNetErr(err)+"). "+
			"Something other than the Dockmesh agent listener answers on this port.")
		return finish()
	}
	peers := tconn.ConnectionState().PeerCertificates
	_ = tconn.Close()
	if len(peers) == 0 {
		add("tls", CheckFail, "The server on "+host+":"+port+" sent no certificate.")
		return finish()
	}
	if s.pki != nil && !s.isOwnListenerCert(peers, host) {
		add("tls", CheckFail, "The server on "+host+":"+port+" presents a certificate from "+issuerName(peers[0])+
			", not the Dockmesh agent CA. A proxy terminates TLS in front of the agent listener (for example a "+
			"reverse proxy, or Cloudflare with proxy mode on). Agents authenticate with client certificates, so "+
			"the connection must reach Dockmesh unchanged: use a DNS-only record, TCP passthrough, or an address "+
			"that bypasses the proxy.")
		return finish()
	}
	add("tls", CheckOK, "The agent listener answers with its own certificate, signed by the agents CA.")
	return finish()
}

func (s *Service) isOwnListenerCert(peers []*x509.Certificate, host string) bool {
	if own, err := s.pki.ServerTLSCertificate(); err == nil && len(own.Certificate) > 0 &&
		bytes.Equal(own.Certificate[0], peers[0].Raw) {
		return true
	}
	inter := x509.NewCertPool()
	for _, c := range peers[1:] {
		inter.AddCert(c)
	}
	_, err := peers[0].Verify(x509.VerifyOptions{
		Roots:         s.pki.CACertPool(),
		Intermediates: inter,
		DNSName:       host,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err == nil
}

func (s *Service) lookupHost(ctx context.Context, host string) ([]string, error) {
	if s.lookup != nil {
		return s.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (s *Service) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if s.dial != nil {
		return s.dial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func isLoopbackName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func issuerName(c *x509.Certificate) string {
	switch {
	case c.Issuer.CommonName != "" && len(c.Issuer.Organization) > 0:
		return fmt.Sprintf("%q (%s)", c.Issuer.CommonName, c.Issuer.Organization[0])
	case c.Issuer.CommonName != "":
		return fmt.Sprintf("%q", c.Issuer.CommonName)
	case len(c.Issuer.Organization) > 0:
		return fmt.Sprintf("%q", c.Issuer.Organization[0])
	}
	return "an unknown issuer"
}

func shortNetErr(err error) string {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "timed out"
	}
	var oerr *net.OpError
	if errors.As(err, &oerr) && oerr.Err != nil {
		return oerr.Err.Error()
	}
	return err.Error()
}

// Cert info enrichment — talks to Caddy's admin API to surface
// per-route certificate metadata (issuer, validity window, days
// remaining). The UI uses this to warn 30 days before expiry; the
// API is best-effort, so a Caddy that's down silently leaves the
// fields blank.
package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"time"
)

// EnrichWithCertInfo populates Cert* fields on each Route by probing
// the live TLS endpoint on the proxy container. We do a real
// handshake against host:443 with SNI = route.Host to get the cert
// the running Caddy is actually serving (which is what users see).
//
// This is best-effort: if the proxy is down, DNS is missing, or the
// port is filtered, we just leave the cert fields blank. The list
// endpoint still works.
func (s *Service) EnrichWithCertInfo(ctx context.Context, routes []Route) []Route {
	out := make([]Route, len(routes))
	for i, r := range routes {
		out[i] = r
		if r.TLSMode == "none" || r.Host == "" {
			continue
		}
		cert, err := probeCert(ctx, r.Host)
		if err != nil {
			continue
		}
		from := cert.NotBefore
		to := cert.NotAfter
		days := int(time.Until(to) / (24 * time.Hour))
		out[i].CertIssuer = certIssuer(cert)
		out[i].CertValidFrom = &from
		out[i].CertValidTo = &to
		out[i].CertDaysLeft = &days
	}
	return out
}

// probeCert opens a TLS connection to host:443 with InsecureSkipVerify
// so we read the cert even if it isn't trusted by our roots (e.g. the
// internal CA Caddy generates for tls_mode=internal).
//
// 4-second timeout — enough for a remote LE cert hop but tight enough
// that a wedged DNS doesn't stall the list endpoint.
func probeCert(ctx context.Context, host string) (*x509.Certificate, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 4 * time.Second},
		Config: &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", host+":443")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("no peer certs in handshake")
	}
	return chain[0], nil
}

// certCacheTTL bounds how often RouteCerts re-probes the routes; cert
// validity changes on the scale of days.
const certCacheTTL = 15 * time.Minute

// RouteCerts returns the routes with cert metadata like
// EnrichWithCertInfo, reusing the last probe for certCacheTTL so pages
// that poll don't open a TLS handshake per route on every request. The
// probe runs on its own deadline, so a caller that goes away can't leave
// a cache without cert data behind.
func (s *Service) RouteCerts(ctx context.Context) ([]Route, error) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	if s.certRoutes != nil && time.Since(s.certAt) < certCacheTTL {
		return s.certRoutes, nil
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	routes, err := s.ListRoutes(pctx)
	if err != nil {
		return nil, err
	}
	routes = s.EnrichWithCertInfo(pctx, routes)
	if pctx.Err() == nil {
		s.certRoutes = routes
		s.certAt = time.Now()
	}
	return routes, nil
}

func certIssuer(c *x509.Certificate) string {
	cn := c.Issuer.CommonName
	if cn != "" {
		return cn
	}
	// Fall back to the first Organization entry — LE uses
	// "Let's Encrypt" as Organization, no CN.
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return c.Issuer.String()
}

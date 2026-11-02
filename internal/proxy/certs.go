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
	"sync"
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
	out, _ := enrichCerts(ctx, routes)
	return out
}

// maxCertProbes caps the TLS handshakes running at once.
const maxCertProbes = 8

// probeCertFn is probeCert; tests replace it.
var probeCertFn = probeCert

// enrichCerts probes the routes at most maxCertProbes at a time. complete
// is false when ctx ended before every probe finished.
func enrichCerts(ctx context.Context, routes []Route) ([]Route, bool) {
	out := make([]Route, len(routes))
	copy(out, routes)
	sem := make(chan struct{}, maxCertProbes)
	var wg sync.WaitGroup
	for i, r := range routes {
		if r.TLSMode == "none" || r.Host == "" {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return out, false
		}
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			defer func() { <-sem }()
			cert, err := probeCertFn(ctx, host)
			if err != nil {
				return
			}
			from, to := cert.NotBefore, cert.NotAfter
			days := int(time.Until(to) / (24 * time.Hour))
			out[i].CertIssuer = certIssuer(cert)
			out[i].CertValidFrom = &from
			out[i].CertValidTo = &to
			out[i].CertDaysLeft = &days
		}(i, r.Host)
	}
	wg.Wait()
	return out, ctx.Err() == nil
}

// probeCert opens a TLS connection to host:443 with InsecureSkipVerify
// so we read the cert even if it isn't trusted by our roots (e.g. the
// internal CA Caddy generates for tls_mode=internal).
//
// 4-second timeout — enough for a remote LE cert hop but tight enough
// that a wedged DNS doesn't stall the list endpoint.
func probeCert(ctx context.Context, host string) (*x509.Certificate, error) {
	return probeCertAt(ctx, net.JoinHostPort(host, "443"), host)
}

// probeCertAt reads the certificate served at addr for the name host.
func probeCertAt(ctx context.Context, addr, host string) (*x509.Certificate, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 4 * time.Second},
		// #nosec G402 -- the certificate is only read, never trusted.
		Config: &tls.Config{ServerName: host, InsecureSkipVerify: true},
	}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn := raw.(*tls.Conn)
	defer conn.Close()
	chain := conn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("no peer certs in handshake")
	}
	return chain[0], nil
}

// certCacheTTL bounds how often RouteCerts re-probes the routes; cert
// validity changes on the scale of days.
const certCacheTTL = 15 * time.Minute

// certRefreshTimeout bounds one background refresh of every route.
var certRefreshTimeout = time.Minute

// RouteCerts returns the routes with cert metadata like
// EnrichWithCertInfo, reusing the last probe for certCacheTTL so pages
// that poll don't open a TLS handshake per route on every request. A
// refresh runs in the background, one at a time; callers wait for it only
// as long as their ctx allows and then get the previous result, or the
// routes without cert metadata. A refresh cut short by its timeout is not
// cached.
func (s *Service) RouteCerts(ctx context.Context) ([]Route, error) {
	s.certMu.Lock()
	if s.certRoutes != nil && time.Since(s.certAt) < certCacheTTL {
		routes := s.certRoutes
		s.certMu.Unlock()
		return routes, nil
	}
	done := s.certRefresh
	if done == nil {
		done = make(chan struct{})
		s.certRefresh = done
		go s.refreshCerts(done)
	}
	stale := s.certRoutes
	s.certMu.Unlock()

	select {
	case <-done:
		s.certMu.Lock()
		routes, fresh := s.certRoutes, time.Since(s.certAt) < certCacheTTL
		s.certMu.Unlock()
		if routes != nil && fresh {
			return routes, nil
		}
	case <-ctx.Done():
	}
	if stale != nil {
		return stale, nil
	}
	return s.ListRoutes(ctx)
}

func (s *Service) refreshCerts(done chan struct{}) {
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), certRefreshTimeout)
	defer cancel()
	routes, err := s.ListRoutes(ctx)
	var enriched []Route
	complete := false
	if err == nil {
		enriched, complete = enrichCerts(ctx, routes)
	}
	s.certMu.Lock()
	defer s.certMu.Unlock()
	if complete {
		s.certRoutes, s.certAt = enriched, time.Now()
	}
	s.certRefresh = nil
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

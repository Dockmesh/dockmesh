package proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Slow probes don't hold callers past their deadline, run at most
// maxCertProbes at a time, and a refresh cut short isn't cached.
func TestRouteCertsDoesNotStallCallers(t *testing.T) {
	ctx := context.Background()
	s := newServiceDB(t)
	for _, h := range []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com",
		"f.example.com", "g.example.com", "h.example.com", "i.example.com", "j.example.com"} {
		if _, err := s.CreateRoute(ctx, RouteSpec{Host: h, Upstream: "10.0.0.2:80", TLSMode: "auto"}); err != nil {
			t.Fatal(err)
		}
	}
	var running, peak atomic.Int32
	release := make(chan struct{})
	prevProbe, prevTimeout := probeCertFn, certRefreshTimeout
	probeCertFn = func(ctx context.Context, _ string) (*x509.Certificate, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
			return &x509.Certificate{NotAfter: time.Now().Add(48 * time.Hour)}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	certRefreshTimeout = 300 * time.Millisecond
	t.Cleanup(func() { probeCertFn, certRefreshTimeout = prevProbe, prevTimeout })

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	routes, err := s.RouteCerts(short)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("caller waited %v for the probes", took)
	}
	for _, r := range routes {
		if r.CertDaysLeft != nil {
			t.Fatal("cert data before any probe finished")
		}
	}
	time.Sleep(400 * time.Millisecond)
	if p := peak.Load(); p > maxCertProbes {
		t.Errorf("%d probes at once, cap is %d", p, maxCertProbes)
	}
	s.certMu.Lock()
	cached := s.certRoutes
	s.certMu.Unlock()
	if cached != nil {
		t.Error("a refresh cut short by its timeout was cached")
	}

	close(release)
	certRefreshTimeout = 5 * time.Second
	routes, err = s.RouteCerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.CertDaysLeft == nil {
			t.Fatalf("route %s without cert data after a full refresh", r.Host)
		}
	}
}

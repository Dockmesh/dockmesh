package setup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestURLGuardCheck(t *testing.T) {
	own := netip.MustParseAddr("192.168.1.10")
	g := urlGuard{ownAddrs: func() []netip.Addr { return []netip.Addr{own, netip.MustParseAddr("fd12::10")} }}
	tests := []struct {
		ip      string
		allowed bool
	}{
		{"93.184.216.34", true},
		{"2606:4700::1111", true},
		{"192.168.1.10", true},
		{"::ffff:192.168.1.10", true},
		{"fd12::10", true},
		{"192.168.1.11", false},
		{"10.0.0.1", false},
		{"172.16.5.5", false},
		{"100.64.0.1", false},
		{"fd00::1", false},
		{"127.0.0.1", false},
		{"127.1.2.3", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"169.254.169.254", false},
		{"169.254.1.1", false},
		{"fe80::1", false},
		{"fd00:ec2::254", false},
		{"100.100.100.200", false},
		{"168.63.129.16", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"255.255.255.255", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			err := g.check(netip.MustParseAddr(tt.ip))
			if (err == nil) != tt.allowed {
				t.Fatalf("check(%s) = %v, want allowed=%v", tt.ip, err, tt.allowed)
			}
		})
	}
}

func TestCheckURLBlocksInternalTargets(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndexByte(srv.URL, ':')+1:]

	tests := []struct {
		name string
		url  string
	}{
		{"loopback literal", srv.URL},
		{"name resolving to loopback", "http://localhost:" + port},
		{"metadata", "http://169.254.169.254/latest/meta-data/"},
		{"private lan host", "http://10.255.255.1:" + port},
	}
	g := urlGuard{ownAddrs: func() []netip.Addr { return nil }}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := checkURL(context.Background(), tt.url, false, g)
			if c.Status != "fail" || c.Reachable || !strings.HasPrefix(c.Message, "not tested") {
				t.Fatalf("got %+v, want blocked", c)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("blocked targets were contacted %d times", n)
	}
}

func TestCheckURLRedirectToBlockedTarget(t *testing.T) {
	// The entry server listens on one of this host's own addresses, which
	// the guard allows; it redirects to a loopback server, which it doesn't.
	g := defaultURLGuard()
	var own netip.Addr
	for _, a := range g.ownAddrs() {
		if a.Is4() && g.check(a) == nil {
			own = a
			break
		}
	}
	if !own.IsValid() {
		t.Skip("no allowed IPv4 interface address on this host")
	}

	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
	}))
	defer internal.Close()

	ln, err := net.Listen("tcp", net.JoinHostPort(own.String(), "0"))
	if err != nil {
		t.Skipf("listen on %s: %v", own, err)
	}
	entry := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/direct" {
			return
		}
		http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
	}))
	entry.Listener = ln
	entry.Start()
	defer entry.Close()

	if c := checkURL(context.Background(), entry.URL+"/direct", false, g); c.Status != "ok" {
		t.Fatalf("own address: got %+v, want ok", c)
	}
	c := checkURL(context.Background(), entry.URL, false, g)
	if c.Status != "fail" || c.Reachable || !strings.HasPrefix(c.Message, "not tested") {
		t.Fatalf("got %+v, want redirect blocked", c)
	}
	if n := internalHits.Load(); n != 0 {
		t.Fatalf("redirect target was contacted %d times", n)
	}
}

package setup

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestURLGuardCheck(t *testing.T) {
	g := urlGuard{
		ownAddrs: func() []netip.Addr {
			return []netip.Addr{netip.MustParseAddr("192.168.1.10"), netip.MustParseAddr("fd12::10")}
		},
		ownPorts: []uint16{80, 443, 8080},
	}
	tests := []struct {
		addr    string
		allowed bool
	}{
		{"93.184.216.34:443", true},
		{"93.184.216.34:22", true},
		{"[2606:4700::1111]:443", true},
		{"192.168.1.10:8080", true},
		{"192.168.1.10:443", true},
		{"[::ffff:192.168.1.10]:80", true},
		{"[fd12::10]:8080", true},
		{"192.168.1.10:22", false},
		{"192.168.1.10:6379", false},
		{"[fd12::10]:5432", false},
		{"192.168.1.11:8080", false},
		{"10.0.0.1:80", false},
		{"172.16.5.5:80", false},
		{"100.64.0.1:80", false},
		{"[fd00::1]:80", false},
		{"127.0.0.1:8080", false},
		{"127.1.2.3:80", false},
		{"[::1]:8080", false},
		{"[::ffff:127.0.0.1]:80", false},
		{"0.0.0.0:80", false},
		{"[::]:80", false},
		{"169.254.169.254:80", false},
		{"169.254.1.1:80", false},
		{"[fe80::1]:80", false},
		{"[fd00:ec2::254]:80", false},
		{"100.100.100.200:80", false},
		{"168.63.129.16:80", false},
		{"224.0.0.1:80", false},
		{"[ff02::1]:80", false},
		{"255.255.255.255:80", false},
		{"[64:ff9b::7f00:1]:80", false},
		{"[64:ff9b::a9fe:a9fe]:80", false},
		{"[64:ff9b:1::a00:1]:80", false},
		{"[::7f00:1]:80", false},
		{"[::a00:1]:80", false},
		{"[fec0::1]:80", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := g.check(netip.MustParseAddrPort(tt.addr))
			if (err == nil) != tt.allowed {
				t.Fatalf("check(%s) = %v, want allowed=%v", tt.addr, err, tt.allowed)
			}
		})
	}
}

func TestContainerInterface(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"docker0", true},
		{"br-3f2a9c1d0e4b", true},
		{"veth12ab34c", true},
		{"eth0", false},
		{"enp3s0", false},
		{"wlan0", false},
		{"docker1", false},
	}
	for _, tt := range tests {
		if got := containerInterface(tt.name); got != tt.want {
			t.Errorf("containerInterface(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHumanizeNetErrHidesRawErrors(t *testing.T) {
	raw := errors.New("dial tcp 10.1.2.3:5432: connect: network is unreachable")
	if got := humanizeNetErr(raw); strings.Contains(got, "10.1.2.3") {
		t.Fatalf("raw dial error echoed: %q", got)
	}
	tlsErr := errors.New("tls: failed to verify certificate: x509: certificate is valid for internal.corp, not x")
	if got := humanizeNetErr(tlsErr); strings.Contains(got, "internal.corp") {
		t.Fatalf("certificate details echoed: %q", got)
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
	var own netip.Addr
	for _, a := range interfaceAddrs() {
		if a.Is4() && !a.IsLoopback() && !a.IsLinkLocalUnicast() {
			own = a
			break
		}
	}
	if !own.IsValid() {
		t.Skip("no usable IPv4 interface address on this host")
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
	g := defaultURLGuard(netip.MustParseAddrPort(ln.Addr().String()).Port())

	if c := checkURL(context.Background(), entry.URL+"/direct", false, g); c.Status != "ok" {
		t.Fatalf("own address: got %+v, want ok", c)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		internalHits.Add(1)
	}))
	otherLn, err := net.Listen("tcp", net.JoinHostPort(own.String(), "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	other.Listener = otherLn
	other.Start()
	defer other.Close()
	if c := checkURL(context.Background(), other.URL, false, g); c.Status != "fail" || !strings.HasPrefix(c.Message, "not tested") {
		t.Fatalf("own address on another port: got %+v, want blocked", c)
	}
	c := checkURL(context.Background(), entry.URL, false, g)
	if c.Status != "fail" || c.Reachable || !strings.HasPrefix(c.Message, "not tested") {
		t.Fatalf("got %+v, want redirect blocked", c)
	}
	if n := internalHits.Load(); n != 0 {
		t.Fatalf("redirect target was contacted %d times", n)
	}
}

package setup

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"syscall"
)

// errBlockedTarget marks a connection the URL test refused to open.
var errBlockedTarget = errors.New("target address not allowed")

// alwaysBlocked are ranges the standard library predicates don't cover:
// cloud metadata endpoints inside otherwise allowed space, reserved IPv4,
// and IPv6 forms that embed an IPv4 address (NAT64, IPv4-compatible) and
// could reach an internal IPv4 host through a translator.
var alwaysBlocked = []netip.Prefix{
	netip.MustParsePrefix("168.63.129.16/32"),   // Azure wireserver
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IMDS over IPv6
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("240.0.0.0/4"), // reserved + broadcast
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("fec0::/10"), // deprecated site-local
}

// sharedAddrSpace is RFC 6598 carrier-grade NAT space, treated like a
// private range.
var sharedAddrSpace = netip.MustParsePrefix("100.64.0.0/10")

// urlGuard decides which addresses the setup URL test may connect to:
// public addresses, plus this host's own private addresses on the ports
// Dockmesh can be served on. The public URL the operator types is normally
// either a public name or this server's own LAN address, so nothing else on
// the internal network, and no other local service, needs to be reachable
// from an unauthenticated endpoint.
type urlGuard struct {
	ownAddrs func() []netip.Addr
	ownPorts []uint16
}

func defaultURLGuard(listenPort uint16) urlGuard {
	ports := []uint16{80, 443}
	if listenPort != 0 {
		ports = append(ports, listenPort)
	}
	return urlGuard{ownAddrs: interfaceAddrs, ownPorts: ports}
}

func (g urlGuard) check(ap netip.AddrPort) error {
	ip := ap.Addr().Unmap()
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", errBlockedTarget)
	}
	for _, p := range alwaysBlocked {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s", errBlockedTarget, ip)
		}
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%w: %s", errBlockedTarget, ip)
	}
	if ip.IsPrivate() || sharedAddrSpace.Contains(ip) {
		if g.isOwn(ip) && slices.Contains(g.ownPorts, ap.Port()) {
			return nil
		}
		return fmt.Errorf("%w: %s is a private address of another machine or service", errBlockedTarget, ap)
	}
	return nil
}

func (g urlGuard) isOwn(ip netip.Addr) bool {
	if g.ownAddrs == nil {
		return false
	}
	for _, own := range g.ownAddrs() {
		if own.Unmap() == ip {
			return true
		}
	}
	return false
}

// control runs after DNS resolution, right before connect, so a name that
// resolves to a public address for a pre-check and to an internal one for
// the real connection is still caught.
func (g urlGuard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlockedTarget, err)
	}
	return g.check(ap)
}

// containerInterface reports interfaces that face containers rather than
// the LAN. Their gateway addresses are this host's, but the containers
// behind them are not, so they don't count as own addresses.
func containerInterface(name string) bool {
	return name == "docker0" || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth")
}

func interfaceAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range ifaces {
		if containerInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

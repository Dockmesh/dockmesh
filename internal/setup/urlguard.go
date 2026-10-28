package setup

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// errBlockedTarget marks a connection the URL test refused to open.
var errBlockedTarget = errors.New("target address not allowed")

// alwaysBlocked are cloud metadata and platform endpoints that sit in
// otherwise allowed ranges (public, CGNAT, ULA).
var alwaysBlocked = []netip.Prefix{
	netip.MustParsePrefix("168.63.129.16/32"),   // Azure wireserver
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IMDS over IPv6
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("240.0.0.0/4"), // reserved + broadcast
}

// sharedAddrSpace is RFC 6598 carrier-grade NAT space, treated like a
// private range.
var sharedAddrSpace = netip.MustParsePrefix("100.64.0.0/10")

// urlGuard decides which addresses the setup URL test may connect to:
// public addresses, plus private addresses that belong to this host. The
// public URL the operator types is normally either a public name or this
// server's own LAN address, so nothing else on the internal network ever
// needs to be reachable from an unauthenticated endpoint.
type urlGuard struct {
	ownAddrs func() []netip.Addr
}

func defaultURLGuard() urlGuard { return urlGuard{ownAddrs: interfaceAddrs} }

func (g urlGuard) check(ip netip.Addr) error {
	ip = ip.Unmap()
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
		if g.ownAddrs != nil {
			for _, own := range g.ownAddrs() {
				if own.Unmap() == ip {
					return nil
				}
			}
		}
		return fmt.Errorf("%w: %s is a private address of another machine", errBlockedTarget, ip)
	}
	return nil
}

// control runs after DNS resolution, right before connect, so a name that
// resolves to a public address for a pre-check and to an internal one for
// the real connection is still caught.
func (g urlGuard) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlockedTarget, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %w", errBlockedTarget, err)
	}
	return g.check(ip)
}

func interfaceAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
			out = append(out, ip.Unmap())
		}
	}
	return out
}

package proxy

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidUpstream = errors.New("upstream must be host:port, e.g. 127.0.0.1:8080 or app.lan:3000")
	ErrAdminUpstream   = errors.New("upstream points at the proxy's own admin API")
)

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?)*$`)

// checkUpstream accepts a plain host:port dial address. Schemes, paths,
// Caddy placeholders and unix sockets are refused, as is the admin API,
// which a route would otherwise publish to anyone who can reach the
// proxy.
func checkUpstream(ctx context.Context, dial string) error {
	host, portStr, err := net.SplitHostPort(dial)
	if err != nil || host == "" {
		return ErrInvalidUpstream
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return ErrInvalidUpstream
	}
	if net.ParseIP(host) == nil && (len(host) > 253 || !hostnameRe.MatchString(host)) {
		return ErrInvalidUpstream
	}
	if targetsAdmin(ctx, dial) {
		return ErrAdminUpstream
	}
	return nil
}

// targetsAdmin reports whether dial reaches Caddy's admin listener,
// which binds the loopback interface of the host network.
func targetsAdmin(ctx context.Context, dial string) bool {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(dial))
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port != adminPort {
		return false
	}
	if host == "" || strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return resolvesToLoopback(ctx, host)
}

// resolvesToLoopback covers names like *.localhost or /etc/hosts
// entries pointing at 127.0.0.1. Lookup failures count as not loopback;
// the route then fails at request time like any unresolvable upstream.
func resolvesToLoopback(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.IP.IsLoopback() || a.IP.IsUnspecified() {
			return true
		}
	}
	return false
}

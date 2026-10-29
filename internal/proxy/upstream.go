package proxy

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrInvalidUpstream = errors.New("upstream must be host:port, e.g. 127.0.0.1:8080 or app.lan:3000")
	ErrAdminUpstream   = errors.New("upstream points at the proxy's own admin API")
	ErrInvalidHost     = errors.New("host must be a domain name or address like app.example.com (localhost, loopback addresses and a bare * are not allowed)")
)

var (
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9_-]*[A-Za-z0-9])?)*$`)
	numericRe  = regexp.MustCompile(`^(0[xX][0-9A-Fa-f]+|[0-9]+)$`)
)

// checkUpstream accepts a plain host:port dial address. Schemes, paths,
// Caddy placeholders, network prefixes (`tcp/`) and unix sockets are
// refused, as is the admin API, which a route would otherwise publish
// to anyone who can reach the proxy.
func checkUpstream(dial string) error {
	host, portStr, err := net.SplitHostPort(dial)
	if err != nil || host == "" {
		return ErrInvalidUpstream
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portStr {
		return ErrInvalidUpstream
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if len(host) > 253 || !hostnameRe.MatchString(host) {
			return ErrInvalidUpstream
		}
		// `127.1`, `2130706433` and `0x7f000001` are IPv4 in inet_aton
		// form to a libc resolver.
		labels := strings.Split(host, ".")
		if numericRe.MatchString(labels[len(labels)-1]) {
			return ErrInvalidUpstream
		}
	}
	// Names are refused on the admin port outright: a DNS check here
	// could be answered differently when Caddy dials.
	if port == adminPort && (ip == nil || ip.IsLoopback() || ip.IsUnspecified()) {
		return ErrAdminUpstream
	}
	return nil
}

// checkRouteHost refuses hosts that would make a route match requests
// addressed to the admin API (its Host allow-list is localhost and
// 127.0.0.1) or catch every request.
func checkRouteHost(host string) error {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	h = strings.TrimPrefix(h, "*.")
	if h == "" || h == "*" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ErrInvalidHost
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() {
			return ErrInvalidHost
		}
		return nil
	}
	if len(h) > 253 || !hostnameRe.MatchString(h) {
		return ErrInvalidHost
	}
	return nil
}

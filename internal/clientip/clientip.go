// Package clientip decides which address a request came from. Forwarding
// headers (X-Forwarded-For, X-Real-IP, X-Forwarded-Proto) are honoured only
// when the TCP peer is a configured trusted proxy; anyone else could forge
// them to dodge rate limits or plant a fake address in sessions and audit.
package clientip

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// DefaultTrusted is used when no trusted proxies are configured. Only a
// process on the same machine can connect from loopback, so a local reverse
// proxy keeps working without configuration.
const DefaultTrusted = "127.0.0.0/8, ::1/128"

type ctxKey struct{}

type info struct {
	ip     string
	secure bool
}

// Resolver resolves client addresses against a trusted-proxy list that can
// change at runtime (admin setting or env).
type Resolver struct {
	source func() string
	// OnUntrustedForward is called when a peer outside the trusted list
	// sends forwarding headers. It runs on the request goroutine and must
	// not block.
	OnUntrustedForward func(peer string)

	mu   sync.Mutex
	raw  string
	nets []netip.Prefix
}

// New returns a resolver reading the comma-separated trusted list from
// source on every request. An empty value means DefaultTrusted.
func New(source func() string) *Resolver {
	return &Resolver{source: source}
}

// ParseList parses a comma- or whitespace-separated list of CIDRs or single
// addresses.
func ParseList(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	}) {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", f)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func (r *Resolver) trusted() []netip.Prefix {
	raw := ""
	if r.source != nil {
		raw = strings.TrimSpace(r.source())
	}
	if raw == "" {
		raw = DefaultTrusted
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if raw != r.raw || r.nets == nil {
		nets, err := ParseList(raw)
		if err != nil {
			// The setting is validated on save; a bad env value falls
			// back to the safe default instead of trusting nothing known.
			nets, _ = ParseList(DefaultTrusted)
		}
		r.raw, r.nets = raw, nets
	}
	return r.nets
}

func contains(nets []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func peerAddr(remote string) (netip.Addr, bool) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func hasForwardHeaders(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != ""
}

// resolve returns the client address and whether the original request used
// TLS.
func (r *Resolver) resolve(req *http.Request) info {
	peer, ok := peerAddr(req.RemoteAddr)
	if !ok {
		return info{ip: req.RemoteAddr, secure: req.TLS != nil}
	}
	nets := r.trusted()
	if !contains(nets, peer) {
		if hasForwardHeaders(req) && r.OnUntrustedForward != nil {
			r.OnUntrustedForward(peer.String())
		}
		return info{ip: peer.String(), secure: req.TLS != nil}
	}

	out := info{ip: peer.String(), secure: req.TLS != nil}
	if strings.EqualFold(strings.TrimSpace(firstValue(req.Header.Get("X-Forwarded-Proto"))), "https") {
		out.secure = true
	}
	// Walk X-Forwarded-For right to left: the rightmost entries were
	// appended by our own proxies, the first untrusted one is the client.
	// Anything left of it is client-supplied and not believed.
	if xff := req.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			out.ip = a.String()
			if !contains(nets, a) {
				break
			}
		}
		return out
	}
	if real := strings.TrimSpace(req.Header.Get("X-Real-IP")); real != "" {
		if a, err := netip.ParseAddr(real); err == nil {
			out.ip = a.Unmap().String()
		}
	}
	return out
}

func firstValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		return v[:i]
	}
	return v
}

// Middleware stores the resolved client address on the request context.
func (r *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		in := r.resolve(req)
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, in)))
	})
}

// IP returns the client address of a request that passed through the
// middleware, or the TCP peer otherwise. Never a forwarding header that
// wasn't vetted.
func IP(r *http.Request) string {
	if in, ok := r.Context().Value(ctxKey{}).(info); ok {
		return in.ip
	}
	if a, ok := peerAddr(r.RemoteAddr); ok {
		return a.String()
	}
	return r.RemoteAddr
}

// Secure reports whether the client reached us over TLS, directly or via a
// trusted proxy that said so.
func Secure(r *http.Request) bool {
	if in, ok := r.Context().Value(ctxKey{}).(info); ok {
		return in.secure
	}
	return r.TLS != nil
}

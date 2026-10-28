package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
)

const noticeKeyUntrustedProxy = "trusted-proxies.unconfigured"

// proxyNotice raises the "configure trusted proxies" notice the first time
// an untrusted peer sends forwarding headers. The flag keeps it to one DB
// write per process instead of one per request.
type proxyNotice struct {
	notices *notices.Service
	raised  atomic.Bool
}

func (p *proxyNotice) observe(peer string) {
	if p == nil || p.notices == nil || !p.raised.CompareAndSwap(false, true) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := p.notices.Raise(ctx, notices.RaiseInput{
			Key:      noticeKeyUntrustedProxy,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    "Dockmesh seems to run behind a proxy: configure trusted proxies",
			Body: fmt.Sprintf("A request from %s carried X-Forwarded-For or X-Real-IP headers, but %s is not a trusted proxy, "+
				"so Dockmesh ignored them. If Dockmesh runs behind a reverse proxy, every user currently shares the proxy's "+
				"address for rate limits, sessions and the audit log: add the proxy's address under Settings → System → "+
				"Trusted proxies (or set DOCKMESH_TRUSTED_PROXIES). Check that %s really is your proxy first; anyone can "+
				"send these headers, and trusting a stranger's address lets them fake their client address.",
				peer, peer, peer),
			Link: "/settings",
		})
		if err != nil {
			slog.Warn("trusted proxies notice", "err", err)
		}
	}()
}

// reset resolves the notice after the trusted-proxy list changed. If the new
// list still doesn't cover the proxy, the next forwarded request raises it
// again.
func (p *proxyNotice) reset(ctx context.Context) {
	if p == nil || p.notices == nil {
		return
	}
	p.raised.Store(false)
	if err := p.notices.Resolve(ctx, noticeKeyUntrustedProxy); err != nil {
		slog.Warn("trusted proxies notice", "err", err)
	}
}

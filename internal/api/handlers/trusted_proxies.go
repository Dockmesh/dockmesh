package handlers

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const noticeKeyUntrustedProxy = "trusted-proxies.unconfigured"

// proxyNotice raises the "configure trusted proxies" notice the first time
// an untrusted peer sends forwarding headers. The flag keeps it to one DB
// write per process instead of one per request; a failed write clears it so
// the next forwarded request retries.
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
			Title:    i18n.Msg("notice.untrusted_proxy.title"),
			Body:     i18n.Msg("notice.untrusted_proxy.body", peer),
			Link:     "/settings",
		})
		if err != nil {
			p.raised.Store(false)
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

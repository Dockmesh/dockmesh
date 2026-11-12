package handlers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const (
	noticeKeyWebInstall   = "agents.web-port-install"
	webInstallNoticeEvery = 24 * time.Hour
	webInstallRetryAfter  = 5 * time.Minute
)

// webInstallNotice tells admins that the legacy web-port installer is in
// use, at most once per webInstallNoticeEvery. Each raise reopens the
// notice, so admins who dismissed it see it again on the next day of use.
type webInstallNotice struct {
	notices *notices.Service
	now     func() time.Time

	mu   sync.Mutex
	last time.Time
}

func newWebInstallNotice(n *notices.Service) *webInstallNotice {
	return &webInstallNotice{notices: n, now: time.Now}
}

// observe records one use of the legacy route from ip. The token is never
// logged.
func (n *webInstallNotice) observe(ctx context.Context, ip string) {
	slog.Warn("agent installer fetched over the deprecated web-port route; use the command from Hosts › Add host", "ip", ip)
	if n == nil || n.notices == nil || !n.due(ctx) {
		return
	}
	in := notices.RaiseInput{
		Key:      noticeKeyWebInstall,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.web_install.title"),
		Body:     i18n.Msg("notice.web_install.body", ip),
		Link:     "/hosts",
	}
	ctx = context.WithoutCancel(ctx)
	if err := n.notices.Resolve(ctx, noticeKeyWebInstall); err != nil {
		slog.Warn("web-port install notice", "err", err)
	}
	if err := n.notices.Raise(ctx, in); err != nil {
		n.mu.Lock()
		n.last = n.now().Add(webInstallRetryAfter - webInstallNoticeEvery)
		n.mu.Unlock()
		slog.Warn("web-port install notice", "err", err)
	}
}

// due reports whether the notice may be raised now and claims the slot.
// After a restart the open notice's age stands in for the in-memory time.
func (n *webInstallNotice) due(ctx context.Context) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if n.last.IsZero() {
		open, err := n.notices.Open(ctx)
		if err != nil {
			slog.Warn("web-port install notice", "err", err)
		}
		for _, o := range open {
			if o.Key == noticeKeyWebInstall {
				n.last = o.CreatedAt
			}
		}
	}
	if !n.last.IsZero() && now.Sub(n.last) < webInstallNoticeEvery {
		return false
	}
	n.last = now
	return true
}

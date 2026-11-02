package alerts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

const (
	// groupWindow collects what one rule announces on one host in quick
	// succession, so a host whose containers all stop at once sends one
	// message instead of one per container.
	groupWindow = 5 * time.Second
	// groupListMax is how many targets a grouped message spells out.
	groupListMax = 10
)

type groupKey struct {
	rule     int64
	host     string
	resolved bool
}

type announcement struct {
	target target
	msg    string
	text   i18n.Text
	value  float64
	ts     time.Time
}

// outGroup is one pending message: the first groupListMax announcements
// plus the total count.
type outGroup struct {
	rule     Rule
	resolved bool
	items    []announcement
	n        int
	level    notify.Level
}

func (g *outGroup) add(a announcement) {
	g.n++
	if len(g.items) < groupListMax {
		g.items = append(g.items, a)
	}
	if !g.resolved && fireLevel(&g.rule, a.value) == notify.LevelCritical {
		g.level = notify.LevelCritical
	}
}

// fireLevel maps a rule's severity to a channel level; a metric breach of
// half again the threshold is critical whatever the severity.
func fireLevel(r *Rule, value float64) notify.Level {
	switch {
	case r.Severity == "critical", r.Kind == KindMetric && r.Operator == "gt" && value >= r.Threshold*1.5:
		return notify.LevelCritical
	case r.Severity == "info":
		return notify.LevelInfo
	}
	return notify.LevelWarning
}

// announce queues a notified fire or resolve for delivery.
func (s *Service) announce(ctx context.Context, r *Rule, a announcement, resolved bool) {
	g := &outGroup{rule: *r, resolved: resolved, level: notify.LevelInfo}
	if !resolved {
		g.level = fireLevel(r, a.value)
	}
	if s.groupWindow <= 0 || s.stopped.Load() {
		g.add(a)
		s.deliver(context.WithoutCancel(ctx), g)
		return
	}
	gk := groupKey{r.ID, a.target.hostID, resolved}
	s.outMu.Lock()
	if cur, ok := s.outbox[gk]; ok {
		g = cur
	} else {
		s.outbox[gk] = g
		later := context.WithoutCancel(ctx)
		time.AfterFunc(s.groupWindow, func() { s.flushGroup(later, gk) })
	}
	g.add(a)
	s.outMu.Unlock()
}

func (s *Service) flushGroup(ctx context.Context, gk groupKey) {
	s.outMu.Lock()
	g := s.outbox[gk]
	delete(s.outbox, gk)
	s.outMu.Unlock()
	if g != nil {
		s.deliver(ctx, g)
	}
}

// flushAll delivers every pending group now.
func (s *Service) flushAll() {
	s.outMu.Lock()
	groups := s.outbox
	s.outbox = map[groupKey]*outGroup{}
	s.outMu.Unlock()
	for _, g := range groups {
		s.deliver(s.sendCtx(), g)
	}
}

func (s *Service) deliver(ctx context.Context, g *outGroup) {
	if s.send != nil {
		s.send(g)
		return
	}
	s.sendGroup(ctx, g)
}

// sendCtx is the context of sends that outlive the evaluation that queued
// them: the service's own, which shutdown does not cancel.
func (s *Service) sendCtx() context.Context {
	if s.baseCtx == nil {
		return context.Background()
	}
	return s.baseCtx
}

// sendGroup pings the rule's channels and, for fires, the bell.
func (s *Service) sendGroup(ctx context.Context, g *outGroup) {
	title, body := s.groupText(ctx, g, i18n.Default)
	r := &g.rule
	if s.notify != nil && len(r.ChannelIDs) > 0 {
		n := notify.Notification{
			Title: title,
			Body:  body,
			Localize: func(lang string) (string, string) {
				return s.groupText(ctx, g, lang)
			},
			Level:     g.level,
			Metric:    r.Metric,
			Threshold: r.Threshold,
			Time:      g.items[0].ts,
		}
		if g.n == 1 {
			n.Container = g.items[0].target.name
			n.Value = g.items[0].value
		}
		s.notify.SendToAll(ctx, r.ChannelIDs, n)
	}
	// The bell keeps a trail in the UI even when the rule has no channel.
	if !g.resolved && s.notifs != nil {
		bellTitle := "Alert firing: " + r.Name
		if g.n > 1 {
			bellTitle += " (" + s.groupSummary(ctx, g, i18n.Default) + ")"
		}
		_, _ = s.notifs.Emit(ctx, alertNotification(r, g.items[0].target.hostID, bellTitle, body))
	}
}

// groupText renders the channel title and body; a group of one reads
// exactly like a single alert.
func (s *Service) groupText(ctx context.Context, g *outGroup, lang string) (string, string) {
	name := g.rule.Name
	if g.n == 1 {
		if g.resolved {
			return i18n.T(lang, "alert.resolved", name), g.items[0].textIn(lang)
		}
		return i18n.T(lang, "alert.fired", name), g.items[0].textIn(lang)
	}
	lines := make([]string, 0, len(g.items)+1)
	for _, a := range g.items {
		lines = append(lines, a.textIn(lang))
	}
	if more := g.n - len(g.items); more > 0 {
		lines = append(lines, i18n.T(lang, "alert.and_more", more))
	}
	summary := s.groupSummary(ctx, g, lang)
	if g.resolved {
		return i18n.T(lang, "alert.resolved_group", name, summary), strings.Join(lines, "\n")
	}
	return i18n.T(lang, "alert.fired_group", name, summary), strings.Join(lines, "\n")
}

// textIn renders the announcement; announcements queued without a text
// keep their English message.
func (a announcement) textIn(lang string) string {
	if a.text.Key == "" {
		return a.msg
	}
	return a.text.In(lang)
}

// groupSummary is "12 containers on dm-agent1".
func (s *Service) groupSummary(ctx context.Context, g *outGroup, lang string) string {
	noun := i18n.T(lang, "alert.noun."+targetNoun(g.rule.Kind))
	if host := s.hostLabel(ctx, g.items[0].target.hostID, lang); host != "" {
		return i18n.T(lang, "alert.group_summary_on", g.n, noun, host)
	}
	return i18n.T(lang, "alert.group_summary", g.n, noun)
}

func targetNoun(kind string) string {
	switch kind {
	case KindHostOffline:
		return "hosts"
	case KindDiskUsage:
		return "mounts"
	case KindBackupFailed, KindBackupMissed:
		return "backup_jobs"
	case KindCertExpiry:
		return "certificates"
	}
	return "containers"
}

// hostLabel names a host for messages: the agent's name, "Local" for the
// server itself, empty for the kinds without a host.
func (s *Service) hostLabel(ctx context.Context, hostID, lang string) string {
	switch hostID {
	case "":
		return ""
	case "local":
		return i18n.T(lang, "alert.host_local")
	}
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, hostID).Scan(&name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Debug("alerts: host name", "host", hostID, "err", err)
		}
		return hostID
	}
	return name
}

// alertNotification addresses a firing alert to the users who can read
// alert history on the alert's host. Backup and certificate rules have no
// host, so like their history entries they only reach unscoped viewers.
func alertNotification(r *Rule, hostID, title, body string) notifications.EmitInput {
	sev := notifications.SevWarning
	switch r.Severity {
	case "critical":
		sev = notifications.SevError
	case "info":
		sev = notifications.SevInfo
	}
	return notifications.EmitInput{
		Kind:     notifications.KindAlertFire,
		Severity: sev,
		Title:    title,
		Body:     body,
		Link:     "/alerts?tab=history",
		Perm:     rbac.PermAlertsHistory,
		HostID:   hostID,
		Unscoped: hostID == "",
	}
}

package agents

import (
	"context"
	"log/slog"
	"strings"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const envMirrorNoticePrefix = "agents.env-mirror."

// SetNotices lets a connecting agent raise its host's notices.
func (s *Service) SetNotices(n *notices.Service) { s.notices = n }

// raiseEnvMirrorNotice tells admins once per host that the agent no longer
// keeps plaintext .env copies. The notice is never resolved, so a later
// connect leaves an open or dismissed one alone.
func (s *Service) raiseEnvMirrorNotice(ctx context.Context, id, name string) {
	if s.notices == nil {
		return
	}
	if err := s.notices.Raise(ctx, notices.RaiseInput{
		Key:      envMirrorNoticePrefix + id,
		Audience: notices.Admins(),
		Severity: notices.SevInfo,
		Title:    i18n.Msg("notice.agent_env_mirror.title", name),
		Body:     i18n.Msg("notice.agent_env_mirror.body", name),
		Link:     "/hosts/" + id,
	}); err != nil {
		slog.Warn("agent .env notice", "agent", name, "err", err)
	}
}

const updateResultNoticePrefix = "agents.update-result."

// updateResultNotice tells admins when a managed agent's root updater
// refused or rolled back an update; a later update that went through
// resolves it.
func (s *Service) updateResultNotice(ctx context.Context, id, name string, res AgentUpdateResult) {
	// The agent writes these fields; keep them to one short line each.
	res.Status, res.To, res.From, res.Step, res.Reason = clip(res.Status, 16), clip(res.To, 64), clip(res.From, 64), clip(res.Step, 32), clip(res.Reason, 300)
	if res.From != "" {
		slog.Info("agent update result", "agent", name, "status", res.Status, "from", res.From, "to", res.To, "step", res.Step, "reason", res.Reason)
	}
	if s.notices == nil {
		return
	}
	key := updateResultNoticePrefix + id
	var title, body i18n.Text
	sev := notices.SevWarning
	switch res.Status {
	case "refused":
		title = i18n.Msg("notice.agent_upd_refused.title", name, res.To)
		body = i18n.Msg("notice.agent_upd_refused.body", res.Reason)
	case "rolled_back":
		title = i18n.Msg("notice.agent_upd_rolled_back.title", name, res.To)
		body = i18n.Msg("notice.agent_upd_rolled_back.body", res.From, res.Reason)
	case "failed":
		sev = notices.SevCritical
		title = i18n.Msg("notice.agent_upd_failed.title", name, res.To)
		body = i18n.Msg("notice.agent_upd_failed.body", res.Reason, "sudo systemctl start dockmesh-agent-update.service")
	default:
		if err := s.notices.Resolve(ctx, key); err != nil {
			slog.Warn("resolve agent update notice", "agent", name, "err", err)
		}
		return
	}
	if err := s.notices.Raise(ctx, notices.RaiseInput{
		Key:      key,
		Audience: notices.Admins(),
		Severity: sev,
		Title:    title,
		Body:     body,
		Link:     "/hosts/" + id,
	}); err != nil {
		slog.Warn("agent update notice", "agent", name, "err", err)
	}
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

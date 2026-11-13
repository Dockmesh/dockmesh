package agents

import (
	"context"
	"log/slog"

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

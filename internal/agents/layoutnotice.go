package agents

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const (
	legacyLayoutNoticeKey = "agents.legacy-layout"
	// legacyLayoutNoticeNames caps the agent names the notice lists.
	legacyLayoutNoticeNames = 10
)

// syncLegacyLayoutNotice is SyncLegacyLayoutNotice with the service's own
// notices, for the events that change the list.
func (s *Service) syncLegacyLayoutNotice(ctx context.Context) {
	if err := s.SyncLegacyLayoutNotice(ctx, s.notices); err != nil {
		slog.Warn("agent legacy layout notice", "err", err)
	}
}

// SyncLegacyLayoutNotice raises one notice naming the Linux agents whose
// last hello reported the legacy layout, and resolves it once none is
// left. Their unit comes from the old installer and only a re-run of the
// install command replaces it.
func (s *Service) SyncLegacyLayoutNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	s.layoutNoticeMu.Lock()
	defer s.layoutNoticeMu.Unlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT name FROM agents
		 WHERE layout = ? AND os = 'linux' AND status NOT IN ('revoked', 'pending', ?)
		 ORDER BY name, id`, LayoutLegacy, StatusRemoved)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, clip(name, 64))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) == 0 {
		return n.Resolve(ctx, legacyLayoutNoticeKey)
	}
	var list any = strings.Join(names, ", ")
	if extra := len(names) - legacyLayoutNoticeNames; extra > 0 {
		list = i18n.Msg("notice.agent_legacy_layout.more", strings.Join(names[:legacyLayoutNoticeNames], ", "), strconv.Itoa(extra))
	}
	body := i18n.Msg("notice.agent_legacy_layout.body", list)
	same, err := n.OpenWithBody(ctx, legacyLayoutNoticeKey, body)
	if err != nil || same {
		return err
	}
	// Raise leaves an open notice as it is, so a changed list replaces it.
	if err := n.Resolve(ctx, legacyLayoutNoticeKey); err != nil {
		return err
	}
	return n.Raise(ctx, notices.RaiseInput{
		Key:      legacyLayoutNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.agent_legacy_layout.title", strconv.Itoa(len(names))),
		Body:     body,
		Link:     "/hosts",
	})
}

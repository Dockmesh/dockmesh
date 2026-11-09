package apitokens

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const legacyNoticeKey = "api-tokens.legacy"

// SyncLegacyNotice raises the admin notice while active legacy tokens exist
// and resolves it once the last one is revoked or expired. Called at
// startup and from every path that changes the set of tokens.
func (s *Service) SyncLegacyNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	count, err := s.CountActiveLegacy(ctx)
	if err != nil {
		return err
	}
	// The title carries no count: raising an open notice is a no-op, so a
	// number would go stale as tokens are replaced one by one.
	return n.Set(ctx, count > 0, notices.RaiseInput{
		Key:      legacyNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.legacy_tokens.title"),
		Body:     i18n.Msg("notice.legacy_tokens.body"),
		Link:     "/tokens",
	})
}

package gitsource

import (
	"context"
	"strings"

	"github.com/dockmesh/dockmesh/internal/notices"
)

const webhookNoticeKey = "git-webhook.no-secret"

// StacksWithoutWebhookSecret lists the stacks whose git source has no
// webhook secret, so incoming webhooks for them are rejected.
func (s *Service) StacksWithoutWebhookSecret(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name FROM stack_git_sources
		 WHERE COALESCE(webhook_secret, '') = ''
		 ORDER BY stack_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// SyncWebhookNotice raises the admin notice while git sources without a
// webhook secret exist and resolves it once every source has one. Called
// at startup and whenever a git source is saved or removed.
func (s *Service) SyncWebhookNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	names, err := s.StacksWithoutWebhookSecret(ctx)
	if err != nil {
		return err
	}
	link := "/stacks"
	if len(names) == 1 {
		link = "/stacks/" + names[0]
	}
	// An open notice keeps its first body, so the list names the stacks
	// affected when it was raised; the stack page shows the live state.
	return n.Set(ctx, len(names) > 0, notices.RaiseInput{
		Key:      webhookNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    "Git webhooks disabled: no webhook secret",
		Body: "Git webhooks now require a secret. These stacks have a git source without one, " +
			"so pushes no longer trigger a sync (polling and manual sync still work): " +
			strings.Join(names, ", ") + ". Open each stack, edit the git source, set a webhook " +
			"secret and enter the same secret in the webhook settings of your git provider.",
		Link: link,
	})
}

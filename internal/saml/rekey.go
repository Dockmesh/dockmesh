package saml

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// RekeyNoticeKey is the admin notice listing accounts still waiting for
// their one-time re-key.
const RekeyNoticeKey = "saml.rekey-pending"

// maxListedRekeys bounds the names the notice lists.
const maxListedRekeys = 20

// PendingRekey is the state of the one-time re-key after the upgrade.
type PendingRekey struct {
	Accounts []string `json:"accounts"`
	Until    string   `json:"until,omitempty"`
}

// PendingRekeys lists the accounts that may still be re-keyed. Marks that
// expired or sit on accounts rekey never takes are dropped first.
func (s *Service) PendingRekeys(ctx context.Context) (*PendingRekey, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE users SET saml_rekey_until = NULL
		 WHERE saml_rekey_until <= CURRENT_TIMESTAMP OR totp_verified <> 0
		    OR EXISTS (SELECT 1 FROM role_assignments a JOIN role_levels l ON l.role_name = a.role_name WHERE a.user_id = users.id AND l.area IN ('users', 'settings') AND l.level = 'manage')`); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT username, saml_rekey_until FROM users WHERE saml_rekey_until IS NOT NULL ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &PendingRekey{Accounts: []string{}}
	for rows.Next() {
		var name, until string
		if err := rows.Scan(&name, &until); err != nil {
			return nil, err
		}
		out.Accounts = append(out.Accounts, name)
		if until > out.Until {
			out.Until = until
		}
	}
	return out, rows.Err()
}

// EndRekey drops every re-key mark and returns how many accounts had one.
func (s *Service) EndRekey(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET saml_rekey_until = NULL WHERE saml_rekey_until IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SyncRekeyNotice raises the admin notice while accounts wait for their
// re-key and resolves it once none do.
func (s *Service) SyncRekeyNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	p, err := s.PendingRekeys(ctx)
	if err != nil {
		return err
	}
	if len(p.Accounts) == 0 {
		return n.Resolve(ctx, RekeyNoticeKey)
	}
	names := p.Accounts
	if len(names) > maxListedRekeys {
		names = names[:maxListedRekeys]
	}
	list := notices.Bullets(names)
	if more := len(p.Accounts) - len(names); more > 0 {
		list = append(list, i18n.Msg("notice.and_more", more))
	}
	until := p.Until
	if len(until) > 10 {
		until = until[:10]
	}
	body := i18n.Msg("notice.saml_rekey.body", len(p.Accounts), until, list)
	if len(p.Accounts) == 1 {
		body = i18n.Msg("notice.saml_rekey.body_one", until, list)
	}
	// An open notice keeps its text on Raise, so reopen it when the list changed.
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.Key == RekeyNoticeKey && o.Body != body.In(i18n.Default) {
			if err := n.Resolve(ctx, RekeyNoticeKey); err != nil {
				return err
			}
		}
	}
	return n.Raise(ctx, notices.RaiseInput{
		Key:      RekeyNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.saml_rekey.title"),
		Body:     body,
		Link:     "/authentication",
	})
}

package auth

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dockmesh/dockmesh/internal/notices"
)

// Execer runs a statement on a database or inside a transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var ssoUnlink = map[string]string{
	"oidc": `UPDATE users SET oidc_provider = NULL, oidc_subject = NULL, updated_at = CURRENT_TIMESTAMP
	          WHERE oidc_provider = ?`,
	"saml": `UPDATE users SET saml_provider = NULL, saml_subject = NULL, saml_rekey_until = NULL, updated_at = CURRENT_TIMESTAMP
	          WHERE saml_provider = ?`,
	"oauth2": `UPDATE users SET oauth2_provider = NULL, oauth2_subject = NULL, oauth2_legacy_subject = NULL,
	                            oauth2_rekey_until = NULL, updated_at = CURRENT_TIMESTAMP
	            WHERE oauth2_provider = ?`,
	"ldap": `UPDATE users SET ldap_provider = NULL, ldap_subject = NULL, ldap_legacy_subject = NULL,
	                          ldap_rekey_until = NULL, updated_at = CURRENT_TIMESTAMP
	          WHERE ldap_provider = ?`,
}

// UnlinkProvider detaches every account from the provider with this slug.
// It runs when the provider is deleted or starts pointing at another
// identity provider, whose subjects mean other people. The accounts stay
// as local accounts without a usable password.
func UnlinkProvider(ctx context.Context, ex Execer, kind, slug string) (int64, error) {
	q, ok := ssoUnlink[kind]
	if !ok {
		return 0, fmt.Errorf("unknown sso kind %q", kind)
	}
	res, err := ex.ExecContext(ctx, q, slug)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ssoRekeyQueries are the statements of the one-time move from a legacy
// (mutable) subject to the immutable one, for the kinds that had one.
type ssoRekeyQueries struct {
	known, candidates, candidatesByEmail, rekey string
}

var ssoRekeyKinds = map[string]ssoRekeyQueries{
	"oauth2": {
		known: `SELECT COUNT(*) FROM users WHERE oauth2_provider = ? AND oauth2_subject = ?`,
		candidates: `SELECT id FROM users WHERE oauth2_provider = ? AND oauth2_legacy_subject = ?
		               AND oauth2_rekey_until > CURRENT_TIMESTAMP`,
		candidatesByEmail: `SELECT id FROM users WHERE oauth2_provider = ? AND oauth2_legacy_subject = ?
		                      AND oauth2_rekey_until > CURRENT_TIMESTAMP AND email = ? COLLATE NOCASE`,
		rekey: `UPDATE users SET oauth2_subject = ?, oauth2_legacy_subject = NULL, oauth2_rekey_until = NULL,
		                         updated_at = CURRENT_TIMESTAMP
		         WHERE id = ? AND oauth2_legacy_subject IS NOT NULL`,
	},
	"ldap": {
		known: `SELECT COUNT(*) FROM users WHERE ldap_provider = ? AND ldap_subject = ?`,
		candidates: `SELECT id FROM users WHERE ldap_provider = ? AND ldap_legacy_subject = ?
		               AND ldap_rekey_until > CURRENT_TIMESTAMP`,
		candidatesByEmail: `SELECT id FROM users WHERE ldap_provider = ? AND ldap_legacy_subject = ?
		                      AND ldap_rekey_until > CURRENT_TIMESTAMP AND email = ? COLLATE NOCASE`,
		rekey: `UPDATE users SET ldap_subject = ?, ldap_legacy_subject = NULL, ldap_rekey_until = NULL,
		                         updated_at = CURRENT_TIMESTAMP
		         WHERE id = ? AND ldap_legacy_subject IS NOT NULL`,
	},
}

// LegacyLogin is one sign-in as seen by the old and the new keying.
type LegacyLogin struct {
	Kind     string
	Provider string
	// Subject is the immutable id the account is keyed by from now on.
	Subject string
	// Legacy is the key the same sign-in produced before the upgrade.
	Legacy string
	// SameSource is true when Legacy and Subject come from the same
	// field, so the legacy key already was the immutable id.
	SameSource bool
	Email      string
}

// RekeyLegacy moves an account still waiting for its re-key to the
// immutable subject when this sign-in presents its legacy key. A legacy
// key read from a renameable field (a login name, a DN) may since belong
// to someone else, so then the account's email has to match as well. It
// returns the re-keyed account's id, or "" when nothing changed.
func (s *Service) RekeyLegacy(ctx context.Context, l LegacyLogin) (string, error) {
	q, ok := ssoRekeyKinds[l.Kind]
	if !ok {
		return "", fmt.Errorf("unknown sso kind %q", l.Kind)
	}
	if l.Subject == "" || l.Legacy == "" {
		return "", nil
	}
	var known int
	if err := s.db.QueryRowContext(ctx, q.known, l.Provider, l.Subject).Scan(&known); err != nil {
		return "", err
	}
	if known > 0 {
		return "", nil
	}
	var rows *sql.Rows
	var err error
	if l.SameSource {
		rows, err = s.db.QueryContext(ctx, q.candidates, l.Provider, l.Legacy)
	} else {
		if l.Email == "" {
			return "", nil
		}
		rows, err = s.db.QueryContext(ctx, q.candidatesByEmail, l.Provider, l.Legacy, l.Email)
	}
	if err != nil {
		return "", err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", nil
	}
	res, err := s.db.ExecContext(ctx, q.rekey, l.Subject, ids[0])
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", nil
	}
	return ids[0], nil
}

// SSORekeyNoticeKey is the admin notice listing OAuth2 and LDAP accounts
// still waiting for their re-key.
const SSORekeyNoticeKey = "sso.rekey-pending"

const maxListedSSORekeys = 20

// PendingSSORekey is the state of the OAuth2/LDAP re-key after the upgrade.
type PendingSSORekey struct {
	Accounts []string `json:"accounts"`
	Until    string   `json:"until,omitempty"`
}

// PendingSSORekeys lists the OAuth2 and LDAP accounts that can still be
// re-keyed. Accounts whose window ended lose their legacy key first; they
// no longer match any sign-in.
func (s *Service) PendingSSORekeys(ctx context.Context) (*PendingSSORekey, error) {
	for _, q := range []string{
		`UPDATE users SET oauth2_legacy_subject = NULL, oauth2_rekey_until = NULL
		  WHERE oauth2_legacy_subject IS NOT NULL AND (oauth2_rekey_until IS NULL OR oauth2_rekey_until <= CURRENT_TIMESTAMP)`,
		`UPDATE users SET ldap_legacy_subject = NULL, ldap_rekey_until = NULL
		  WHERE ldap_legacy_subject IS NOT NULL AND (ldap_rekey_until IS NULL OR ldap_rekey_until <= CURRENT_TIMESTAMP)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT username, oauth2_rekey_until FROM users WHERE oauth2_legacy_subject IS NOT NULL
		UNION ALL
		SELECT username, ldap_rekey_until FROM users WHERE ldap_legacy_subject IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &PendingSSORekey{Accounts: []string{}}
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

// EndSSORekey drops every pending OAuth2/LDAP re-key and returns how many
// accounts had one.
func (s *Service) EndSSORekey(ctx context.Context) (int64, error) {
	var total int64
	for _, q := range []string{
		`UPDATE users SET oauth2_legacy_subject = NULL, oauth2_rekey_until = NULL WHERE oauth2_legacy_subject IS NOT NULL`,
		`UPDATE users SET ldap_legacy_subject = NULL, ldap_rekey_until = NULL WHERE ldap_legacy_subject IS NOT NULL`,
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// SyncSSORekeyNotice raises the admin notice while OAuth2 or LDAP accounts
// wait for their re-key and resolves it once none do.
func (s *Service) SyncSSORekeyNotice(ctx context.Context, n *notices.Service) error {
	if n == nil {
		return nil
	}
	p, err := s.PendingSSORekeys(ctx)
	if err != nil {
		return err
	}
	if len(p.Accounts) == 0 {
		return n.Resolve(ctx, SSORekeyNoticeKey)
	}
	names := p.Accounts
	if len(names) > maxListedSSORekeys {
		names = append(names[:maxListedSSORekeys:maxListedSSORekeys], fmt.Sprintf("and %d more", len(p.Accounts)-maxListedSSORekeys))
	}
	until := p.Until
	if len(until) > 10 {
		until = until[:10]
	}
	intro := fmt.Sprintf("These %d accounts signed in through OAuth2 or LDAP before the upgrade and have not signed in since. "+
		"Each moves to its provider's immutable user id at its next sign-in, until %s:", len(p.Accounts), until)
	if len(p.Accounts) == 1 {
		intro = fmt.Sprintf("This account signed in through OAuth2 or LDAP before the upgrade and has not signed in since. "+
			"It moves to its provider's immutable user id at its next sign-in, until %s:", until)
	}
	body := intro + notices.Bullets(names) +
		"A sign-in whose login name or directory entry changed since needs the same email as the account. " +
		"After that date, or when you end re-keying, these accounts can no longer sign in through their provider; " +
		"an admin then has to remove and recreate them."
	// An open notice keeps its text on Raise, so reopen it when the list changed.
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.Key == SSORekeyNoticeKey && o.Body != body {
			if err := n.Resolve(ctx, SSORekeyNoticeKey); err != nil {
				return err
			}
		}
	}
	return n.Raise(ctx, notices.RaiseInput{
		Key:      SSORekeyNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    "OAuth2 and LDAP accounts not re-keyed yet",
		Body:     body,
		Link:     "/authentication",
	})
}

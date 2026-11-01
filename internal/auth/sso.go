package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/rbac"

	"github.com/google/uuid"
)

var (
	// ErrSSOEmailInUse means a Dockmesh account already uses the IdP's email
	// but isn't linked to this IdP identity, and linking by email is not
	// allowed for this login.
	ErrSSOEmailInUse = errors.New("an account with this email already exists; an admin must enable linking by verified email for this provider, or remove the existing account")
	// ErrSSOAutoCreateOff means the identity is unknown and sign-in policy
	// doesn't create accounts on first SSO login.
	ErrSSOAutoCreateOff = errors.New("no Dockmesh account is linked to this identity and automatic account creation is turned off")
)

// Role sources: a role set by an admin stays put; a role that came from
// the IdP follows the IdP's group mapping on every login.
const (
	RoleSourceManual = "manual"
	RoleSourceSSO    = "sso"
)

// ssoQueries holds, per SSO kind, the statements that read and write the
// identity link on users and the provider's email-linking opt-in. They are
// literals so no table or column name is ever built at runtime.
type ssoQueries struct {
	bySubject, link, insert, linkingBySlug, linkingAll, setLinking string
}

var ssoKinds = map[string]ssoQueries{
	"oidc": {
		bySubject:     `SELECT id FROM users WHERE oidc_provider = ? AND oidc_subject = ? LIMIT 1`,
		link:          `UPDATE users SET oidc_provider = ?, oidc_subject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		insert:        `INSERT INTO users (id, username, email, password, role_source, oidc_provider, oidc_subject) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		linkingBySlug: `SELECT allow_email_link FROM oidc_providers WHERE slug = ?`,
		linkingAll:    `SELECT id, allow_email_link FROM oidc_providers`,
		setLinking:    `UPDATE oidc_providers SET allow_email_link = ? WHERE id = ?`,
	},
	"saml": {
		bySubject:     `SELECT id FROM users WHERE saml_provider = ? AND saml_subject = ? LIMIT 1`,
		link:          `UPDATE users SET saml_provider = ?, saml_subject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		insert:        `INSERT INTO users (id, username, email, password, role_source, saml_provider, saml_subject) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		linkingBySlug: `SELECT allow_email_link FROM saml_providers WHERE slug = ?`,
		linkingAll:    `SELECT id, allow_email_link FROM saml_providers`,
		setLinking:    `UPDATE saml_providers SET allow_email_link = ? WHERE id = ?`,
	},
	"oauth2": {
		bySubject:     `SELECT id FROM users WHERE oauth2_provider = ? AND oauth2_subject = ? LIMIT 1`,
		link:          `UPDATE users SET oauth2_provider = ?, oauth2_subject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		insert:        `INSERT INTO users (id, username, email, password, role_source, oauth2_provider, oauth2_subject) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		linkingBySlug: `SELECT allow_email_link FROM oauth2_providers WHERE slug = ?`,
		linkingAll:    `SELECT id, allow_email_link FROM oauth2_providers`,
		setLinking:    `UPDATE oauth2_providers SET allow_email_link = ? WHERE id = ?`,
	},
	"ldap": {
		bySubject:     `SELECT id FROM users WHERE ldap_provider = ? AND ldap_subject = ? LIMIT 1`,
		link:          `UPDATE users SET ldap_provider = ?, ldap_subject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		insert:        `INSERT INTO users (id, username, email, password, role_source, ldap_provider, ldap_subject) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		linkingBySlug: `SELECT allow_email_link FROM ldap_providers WHERE slug = ?`,
		linkingAll:    `SELECT id, allow_email_link FROM ldap_providers`,
		setLinking:    `UPDATE ldap_providers SET allow_email_link = ? WHERE id = ?`,
	},
}

func ssoKind(kind string) (ssoQueries, error) {
	q, ok := ssoKinds[kind]
	if !ok {
		return ssoQueries{}, fmt.Errorf("unknown sso kind %q", kind)
	}
	return q, nil
}

// SSOIdentity is what an identity provider asserted for one login.
type SSOIdentity struct {
	Kind     string // oidc | saml | oauth2 | ldap
	Provider string // provider slug
	Subject  string
	Email    string
	// EmailVerified is true only when the IdP explicitly says so.
	EmailVerified bool
	Username      string
	// Assignments the IdP's group mappings (or its default role) give.
	Assignments []rbac.Assignment
}

// ClaimTrue reads a boolean IdP claim; some IdPs send "true" as a string.
func ClaimTrue(claims map[string]any, key string) bool {
	switch v := claims[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// SetEmailLinking stores a provider's opt-in to attach an IdP identity to
// an existing account with the same, verified email.
func (s *Service) SetEmailLinking(ctx context.Context, kind string, providerID int64, allow bool) error {
	q, err := ssoKind(kind)
	if err != nil {
		return err
	}
	v := 0
	if allow {
		v = 1
	}
	_, err = s.db.ExecContext(ctx, q.setLinking, v, providerID)
	return err
}

// EmailLinkingByID returns the email-linking opt-in of every provider of
// kind, keyed by provider id.
func (s *Service) EmailLinkingByID(ctx context.Context, kind string) (map[int64]bool, error) {
	q, err := ssoKind(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, q.linkingAll)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		var allow int
		if err := rows.Scan(&id, &allow); err != nil {
			return nil, err
		}
		out[id] = allow == 1
	}
	return out, rows.Err()
}

// ProvisionSSOUser finds or creates the account for an SSO login. Known
// identities sign in; an existing account with the same email is linked
// only when the email is verified and the provider allows linking; new
// accounts are created only when sign-in policy allows it. Access is
// updated from the IdP only for accounts whose source is "sso".
func (s *Service) ProvisionSSOUser(ctx context.Context, id SSOIdentity) (*User, error) {
	q, err := ssoKind(id.Kind)
	if err != nil {
		return nil, err
	}
	if id.Subject == "" {
		return nil, errors.New("identity provider returned no subject")
	}
	userID, err := s.lookupUserID(ctx, q.bySubject, id.Provider, id.Subject)
	if err != nil {
		return nil, err
	}
	if userID == "" && id.Email != "" {
		// Only plain local accounts can be linked: one already bound to an
		// IdP would be taken from it, and an account with 2FA or a role
		// that manages users or settings would be entered without its own
		// factors.
		emailUser, err := s.lookupUserID(ctx, `SELECT id FROM users WHERE email = ? COLLATE NOCASE LIMIT 1`, id.Email)
		if err != nil {
			return nil, err
		}
		if emailUser != "" {
			linkable, err := s.lookupUserID(ctx, `
				SELECT id FROM users WHERE id = ? AND totp_verified = 0
				   AND NOT EXISTS (SELECT 1 FROM role_assignments a JOIN role_levels l ON l.role_name = a.role_name WHERE a.user_id = users.id AND l.area IN ('users', 'settings') AND l.level = 'manage')
				   AND oidc_provider IS NULL AND saml_provider IS NULL
				   AND oauth2_provider IS NULL AND ldap_provider IS NULL`, emailUser)
			if err != nil {
				return nil, err
			}
			if linkable == "" {
				return nil, ErrSSOEmailInUse
			}
		}
		if emailUser != "" {
			var allow int
			if err := s.db.QueryRowContext(ctx, q.linkingBySlug, id.Provider).Scan(&allow); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if !id.EmailVerified || allow != 1 {
				return nil, ErrSSOEmailInUse
			}
			if _, err := s.db.ExecContext(ctx, q.link, id.Provider, id.Subject, emailUser); err != nil {
				return nil, err
			}
			userID = emailUser
		}
	}
	if userID == "" {
		if !s.signInConfig().AutoCreateOnSSO {
			return nil, ErrSSOAutoCreateOff
		}
		return s.createSSOUser(ctx, id, q.insert)
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Source == RoleSourceSSO {
		if err := s.syncIdPAssignments(ctx, userID, id.Assignments); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (s *Service) lookupUserID(ctx context.Context, query string, args ...any) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// createSSOUser inserts an account without a usable password, retrying
// with a numeric suffix when the username is taken.
func (s *Service) createSSOUser(ctx context.Context, id SSOIdentity, insert string) (*User, error) {
	randomPw, err := generatePassword(40)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(randomPw)
	if err != nil {
		return nil, err
	}
	var email any
	if id.Email != "" {
		email = id.Email
	}
	base := id.Username
	if base == "" {
		base = strings.Split(id.Email, "@")[0]
	}
	for i := 1; i < 100; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s%d", base, i)
		}
		uid := uuid.NewString()
		_, err = s.db.ExecContext(ctx, insert, uid, name, email, hash, RoleSourceSSO, id.Provider, id.Subject)
		if err == nil {
			if err := s.syncIdPAssignments(ctx, uid, id.Assignments); err != nil {
				return nil, err
			}
			return s.GetUser(ctx, uid)
		}
		if !strings.Contains(err.Error(), "users.username") {
			return nil, fmt.Errorf("create sso user: %w", err)
		}
	}
	return nil, fmt.Errorf("create sso user: %w", err)
}

func (s *Service) syncIdPAssignments(ctx context.Context, userID string, want []rbac.Assignment) error {
	if s.syncIdP == nil {
		return errors.New("identity provider assignments are not wired")
	}
	return s.syncIdP(ctx, userID, want)
}

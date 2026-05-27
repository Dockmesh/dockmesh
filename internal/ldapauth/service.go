// Package ldapauth provides LDAP / Active Directory bind-and-search
// authentication for SSO login. It sits alongside internal/oidc and
// internal/saml; the three packages share the user-row JIT pattern
// (extra columns on users — ldap_provider / ldap_subject — link the
// local row to the directory entry).
//
// Package name avoids stdlib's reserved "ldap" so import paths stay
// unambiguous if Go ever ships a stdlib LDAP package.
package ldapauth

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/secrets"

	"github.com/go-ldap/ldap/v3"
)

var (
	ErrProviderNotFound   = errors.New("ldap provider not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailRequired      = errors.New("user has no email attribute")
)

type GroupMapping struct {
	GroupValue string `json:"group"`
	RoleName   string `json:"role"`
}

// Provider mirrors the LDAPConfig type in
// web/src/routes/authentication/_types.ts so create/update payloads
// can flow through without renaming. tls_mode replaces the
// historical (use_tls, use_starttls) two-bool pair — the frontend's
// ldaps | starttls | none enum forbids the illegal "both set" state.
type Provider struct {
	ID                       int64          `json:"id"`
	Slug                     string         `json:"slug"`
	DisplayName              string         `json:"display_name"`
	Host                     string         `json:"host"`
	Port                     int            `json:"port"`
	TLSMode                  string         `json:"tls"` // ldaps | starttls | none
	SkipVerify               bool           `json:"skip_verify"`
	BindDN                   string         `json:"bind_dn,omitempty"`
	UserSearchBase           string         `json:"user_search_base"`
	UserSearchFilter         string         `json:"user_search_filter"`
	UsernameAttribute        string         `json:"username_attribute"`
	EmailAttribute           string         `json:"email_attribute"`
	GroupSearchBase          string         `json:"group_search_base,omitempty"`
	GroupMembershipAttribute string         `json:"group_membership_attribute"`
	DefaultRole              string         `json:"default_role"`
	GroupMappings            []GroupMapping `json:"group_mappings"`
	Enabled                  bool           `json:"enabled"`
	IsDefault                bool           `json:"is_default"`
	LastTestedAt             *time.Time     `json:"last_tested_at,omitempty"`
	LastTestOK               *bool          `json:"last_test_ok,omitempty"`
	LastTestError            string         `json:"last_test_error,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

type ProviderInput struct {
	Slug                     string         `json:"slug"`
	DisplayName              string         `json:"display_name"`
	Host                     string         `json:"host"`
	Port                     int            `json:"port"`
	TLSMode                  string         `json:"tls"`
	SkipVerify               bool           `json:"skip_verify"`
	BindDN                   string         `json:"bind_dn"`
	BindPassword             string         `json:"bind_password"`
	UserSearchBase           string         `json:"user_search_base"`
	UserSearchFilter         string         `json:"user_search_filter"`
	UsernameAttribute        string         `json:"username_attribute"`
	EmailAttribute           string         `json:"email_attribute"`
	GroupSearchBase          string         `json:"group_search_base"`
	GroupMembershipAttribute string         `json:"group_membership_attribute"`
	DefaultRole              string         `json:"default_role"`
	GroupMappings            []GroupMapping `json:"group_mappings,omitempty"`
	Enabled                  bool           `json:"enabled"`
	IsDefault                bool           `json:"is_default,omitempty"`
}

type PublicProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type Service struct {
	db      *sql.DB
	auth    *auth.Service
	secrets *secrets.Service
	mu      sync.Mutex
}

func NewService(db *sql.DB, authSvc *auth.Service, secretsSvc *secrets.Service) *Service {
	return &Service{db: db, auth: authSvc, secrets: secretsSvc}
}

// -----------------------------------------------------------------------------
// Provider CRUD
// -----------------------------------------------------------------------------

const selectColumns = `id, slug, display_name, host, port, tls_mode,
	skip_verify, COALESCE(bind_dn, ''), user_search_base, user_search_filter,
	username_attribute, email_attribute,
	COALESCE(group_search_base, ''), group_membership_attribute,
	default_role, enabled, is_default,
	last_tested_at, last_test_ok, last_test_error,
	created_at, updated_at`

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectColumns+` FROM ldap_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		p, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.loadGroupMappings(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) ListEnabledPublic(ctx context.Context) ([]PublicProvider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, display_name FROM ldap_providers WHERE enabled = 1 ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicProvider{}
	for rows.Next() {
		var p PublicProvider
		if err := rows.Scan(&p.Slug, &p.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) GetProvider(ctx context.Context, id int64) (*Provider, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectColumns+` FROM ldap_providers WHERE id = ?`, id)
	p, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProviderNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadGroupMappings(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// getProviderInternalBySlug returns the provider plus its decrypted
// bind password, used by the live login flow. The password is never
// exposed on the public API surface.
func (s *Service) getProviderInternalBySlug(ctx context.Context, slug string) (*Provider, string, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+selectColumns+`, COALESCE(bind_password, '') FROM ldap_providers WHERE slug = ? AND enabled = 1`, slug)
	p, bindPW, err := scanRowWithSecret(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrProviderNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if err := s.loadGroupMappings(ctx, p); err != nil {
		return nil, "", err
	}
	pw, derr := s.decryptSecret(bindPW)
	if derr != nil {
		return nil, "", fmt.Errorf("decrypt bind_password: %w", derr)
	}
	return p, pw, nil
}

func (s *Service) getProviderInternalByID(ctx context.Context, id int64) (*Provider, string, error) {
	var slug string
	if err := s.db.QueryRowContext(ctx,
		`SELECT slug FROM ldap_providers WHERE id = ?`, id).Scan(&slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrProviderNotFound
		}
		return nil, "", err
	}
	return s.getProviderInternalBySlug(ctx, slug)
}

func (s *Service) CreateProvider(ctx context.Context, in ProviderInput) (*Provider, error) {
	if in.Slug == "" || in.Host == "" || in.UserSearchBase == "" {
		return nil, errors.New("slug, host, and user_search_base required")
	}
	in = applyDefaults(in)
	enc, err := s.encryptSecret(in.BindPassword)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO ldap_providers
			(slug, display_name, host, port, tls_mode, skip_verify,
			 bind_dn, bind_password, user_search_base, user_search_filter,
			 username_attribute, email_attribute,
			 group_search_base, group_membership_attribute,
			 default_role, enabled, is_default)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.Slug, in.DisplayName, in.Host, in.Port, in.TLSMode, boolInt(in.SkipVerify),
		nullable(in.BindDN), nullable(enc),
		in.UserSearchBase, in.UserSearchFilter,
		in.UsernameAttribute, in.EmailAttribute,
		nullable(in.GroupSearchBase), in.GroupMembershipAttribute,
		in.DefaultRole, boolInt(in.Enabled), boolInt(in.IsDefault))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a provider with that slug already exists")
		}
		return nil, fmt.Errorf("insert ldap provider: %w", err)
	}
	id, _ := res.LastInsertId()
	if len(in.GroupMappings) > 0 {
		if err := s.SetGroupMappings(ctx, id, in.GroupMappings); err != nil {
			return nil, err
		}
	}
	return s.GetProvider(ctx, id)
}

func (s *Service) UpdateProvider(ctx context.Context, id int64, in ProviderInput) (*Provider, error) {
	in = applyDefaults(in)
	if in.BindPassword == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE ldap_providers SET
				display_name = ?, host = ?, port = ?, tls_mode = ?, skip_verify = ?,
				bind_dn = ?, user_search_base = ?, user_search_filter = ?,
				username_attribute = ?, email_attribute = ?,
				group_search_base = ?, group_membership_attribute = ?,
				default_role = ?, enabled = ?, is_default = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.Host, in.Port, in.TLSMode, boolInt(in.SkipVerify),
			nullable(in.BindDN), in.UserSearchBase, in.UserSearchFilter,
			in.UsernameAttribute, in.EmailAttribute,
			nullable(in.GroupSearchBase), in.GroupMembershipAttribute,
			in.DefaultRole, boolInt(in.Enabled), boolInt(in.IsDefault), id)
		if err != nil {
			return nil, err
		}
	} else {
		enc, err := s.encryptSecret(in.BindPassword)
		if err != nil {
			return nil, err
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE ldap_providers SET
				display_name = ?, host = ?, port = ?, tls_mode = ?, skip_verify = ?,
				bind_dn = ?, bind_password = ?,
				user_search_base = ?, user_search_filter = ?,
				username_attribute = ?, email_attribute = ?,
				group_search_base = ?, group_membership_attribute = ?,
				default_role = ?, enabled = ?, is_default = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.Host, in.Port, in.TLSMode, boolInt(in.SkipVerify),
			nullable(in.BindDN), nullable(enc),
			in.UserSearchBase, in.UserSearchFilter,
			in.UsernameAttribute, in.EmailAttribute,
			nullable(in.GroupSearchBase), in.GroupMembershipAttribute,
			in.DefaultRole, boolInt(in.Enabled), boolInt(in.IsDefault), id)
		if err != nil {
			return nil, err
		}
	}
	if in.GroupMappings != nil {
		if err := s.SetGroupMappings(ctx, id, in.GroupMappings); err != nil {
			return nil, err
		}
	}
	return s.GetProvider(ctx, id)
}

func (s *Service) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ldap_providers WHERE id = ?`, id)
	return err
}

func (s *Service) SetGroupMappings(ctx context.Context, providerID int64, mappings []GroupMapping) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ldap_provider_group_mappings WHERE provider_id = ?`, providerID); err != nil {
		return err
	}
	for _, m := range mappings {
		if m.GroupValue == "" || m.RoleName == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO ldap_provider_group_mappings (provider_id, group_value, role_name)
			 VALUES (?, ?, ?)`, providerID, m.GroupValue, m.RoleName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) loadGroupMappings(ctx context.Context, p *Provider) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_value, role_name FROM ldap_provider_group_mappings
		  WHERE provider_id = ? ORDER BY group_value`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	p.GroupMappings = []GroupMapping{}
	for rows.Next() {
		var m GroupMapping
		if err := rows.Scan(&m.GroupValue, &m.RoleName); err != nil {
			return err
		}
		p.GroupMappings = append(p.GroupMappings, m)
	}
	return rows.Err()
}

func (s *Service) RecordProviderTest(ctx context.Context, providerID int64, ok bool, errMsg string) error {
	okInt := 0
	if ok {
		okInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE ldap_providers SET last_tested_at = CURRENT_TIMESTAMP,
		                            last_test_ok = ?, last_test_error = ?
		   WHERE id = ?`, okInt, nullable(errMsg), providerID)
	return err
}

// -----------------------------------------------------------------------------
// Connection + login flow
// -----------------------------------------------------------------------------

// dial opens a connection per the provider's tls_mode.
func (s *Service) dial(p *Provider) (*ldap.Conn, error) {
	addr := fmt.Sprintf("%s:%d", p.Host, p.Port)
	tlsCfg := &tls.Config{
		InsecureSkipVerify: p.SkipVerify,
		ServerName:         p.Host,
	}
	switch p.TLSMode {
	case "ldaps":
		return ldap.DialURL("ldaps://"+addr, ldap.DialWithTLSConfig(tlsCfg))
	case "starttls":
		conn, err := ldap.DialURL("ldap://" + addr)
		if err != nil {
			return nil, err
		}
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	default:
		return ldap.DialURL("ldap://" + addr)
	}
}

// TestConnection opens a connection and binds with the stored
// service-account credentials (or anonymous if none).
func (s *Service) TestConnection(ctx context.Context, providerID int64) error {
	p, bindPW, err := s.getProviderInternalByID(ctx, providerID)
	if err != nil {
		return err
	}
	conn, err := s.dial(p)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if p.BindDN != "" {
		if err := conn.Bind(p.BindDN, bindPW); err != nil {
			return fmt.Errorf("bind: %w", err)
		}
	}
	return nil
}

// Authenticate validates the user's password against the directory.
// Two-step: bind as service account, search for the user DN, then
// re-bind as the user with their plaintext password.
func (s *Service) Authenticate(ctx context.Context, slug, username, password string, userAgent, ip string) (*auth.LoginResult, error) {
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	p, bindPW, err := s.getProviderInternalBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	conn, err := s.dial(p)
	if err != nil {
		return nil, fmt.Errorf("ldap dial: %w", err)
	}
	defer conn.Close()

	if p.BindDN != "" {
		if err := conn.Bind(p.BindDN, bindPW); err != nil {
			return nil, fmt.Errorf("service bind: %w", err)
		}
	}
	filter := strings.ReplaceAll(p.UserSearchFilter, "%s", ldap.EscapeFilter(username))
	res, err := conn.Search(ldap.NewSearchRequest(
		p.UserSearchBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		1, 5, false, filter,
		[]string{p.EmailAttribute, p.UsernameAttribute, p.GroupMembershipAttribute},
		nil,
	))
	if err != nil {
		return nil, fmt.Errorf("user search: %w", err)
	}
	if len(res.Entries) == 0 {
		return nil, ErrInvalidCredentials
	}
	entry := res.Entries[0]
	dn := entry.DN

	if err := conn.Bind(dn, password); err != nil {
		return nil, ErrInvalidCredentials
	}

	email := entry.GetAttributeValue(p.EmailAttribute)
	if email == "" {
		return nil, ErrEmailRequired
	}
	name := entry.GetAttributeValue(p.UsernameAttribute)
	if name == "" {
		name = username
	}
	groups := entry.GetAttributeValues(p.GroupMembershipAttribute)

	role := p.DefaultRole
	for _, g := range groups {
		// AD typically returns full DNs in memberOf — match exact
		// first, then by CN prefix as a convenience for admins
		// who'd rather configure "Admins" than the full DN.
		for _, m := range p.GroupMappings {
			if g == m.GroupValue || strings.HasPrefix(strings.ToLower(g), "cn="+strings.ToLower(m.GroupValue)+",") {
				role = m.RoleName
				break
			}
		}
	}

	user, err := s.jitProvision(ctx, p, dn, name, email, role)
	if err != nil {
		return nil, err
	}
	return s.auth.StartSessionForSSO(ctx, *user, userAgent, ip)
}

func (s *Service) jitProvision(ctx context.Context, p *Provider, subjectDN, name, email, role string) (*auth.User, error) {
	var userID, username string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username FROM users
		 WHERE (ldap_provider = ? AND ldap_subject = ?)
		    OR email = ?
		 LIMIT 1`, p.Slug, subjectDN, email).Scan(&userID, &username)
	if err != nil {
		u, cerr := s.auth.CreateSSOUser(ctx, name, email, role, p.Slug, subjectDN)
		if cerr != nil {
			for i := 2; i < 100; i++ {
				u, cerr = s.auth.CreateSSOUser(ctx, fmt.Sprintf("%s%d", name, i), email, role, p.Slug, subjectDN)
				if cerr == nil {
					break
				}
			}
			if cerr != nil {
				return nil, fmt.Errorf("create sso user: %w", cerr)
			}
		}
		_, _ = s.db.ExecContext(ctx,
			`UPDATE users SET ldap_provider = ?, ldap_subject = ?,
			                  oidc_provider = NULL, oidc_subject = NULL
			   WHERE id = ?`, p.Slug, subjectDN, u.ID)
		return u, nil
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE users SET ldap_provider = ?, ldap_subject = ?, role = ?,
		                 updated_at = CURRENT_TIMESTAMP
		   WHERE id = ?`, p.Slug, subjectDN, role, userID)
	if err != nil {
		return nil, err
	}
	return &auth.User{ID: userID, Username: username, Email: email, Role: role}, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func applyDefaults(in ProviderInput) ProviderInput {
	if in.TLSMode == "" {
		in.TLSMode = "ldaps"
	}
	if in.Port == 0 {
		if in.TLSMode == "ldaps" {
			in.Port = 636
		} else {
			in.Port = 389
		}
	}
	if in.UserSearchFilter == "" {
		in.UserSearchFilter = "(&(objectClass=person)(uid=%s))"
	}
	if in.UsernameAttribute == "" {
		in.UsernameAttribute = "uid"
	}
	if in.EmailAttribute == "" {
		in.EmailAttribute = "mail"
	}
	if in.GroupMembershipAttribute == "" {
		in.GroupMembershipAttribute = "memberOf"
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "viewer"
	}
	return in
}

func (s *Service) encryptSecret(plain string) (string, error) {
	if plain == "" || s.secrets == nil || !s.secrets.Enabled() {
		return plain, nil
	}
	ct, err := s.secrets.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Service) decryptSecret(stored string) (string, error) {
	if stored == "" || s.secrets == nil || !s.secrets.Enabled() {
		return stored, nil
	}
	ct, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		// Legacy plaintext path.
		return stored, nil
	}
	pt, err := s.secrets.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scannerIface interface{ Scan(dest ...any) error }

// scanRow reads the 22 columns produced by selectColumns into a Provider.
func scanRow(r scannerIface) (*Provider, error) {
	var p Provider
	var lastTestErr sql.NullString
	var enabled, isDefault, skipVerify int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.Host, &p.Port, &p.TLSMode,
		&skipVerify, &p.BindDN, &p.UserSearchBase, &p.UserSearchFilter,
		&p.UsernameAttribute, &p.EmailAttribute,
		&p.GroupSearchBase, &p.GroupMembershipAttribute,
		&p.DefaultRole, &enabled, &isDefault,
		&lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.SkipVerify = skipVerify == 1
	p.Enabled = enabled == 1
	p.IsDefault = isDefault == 1
	if lastTestedAt.Valid {
		t := lastTestedAt.Time
		p.LastTestedAt = &t
	}
	if lastTestOK.Valid {
		ok := lastTestOK.Int64 == 1
		p.LastTestOK = &ok
	}
	if lastTestErr.Valid {
		p.LastTestError = lastTestErr.String
	}
	p.GroupMappings = []GroupMapping{}
	return &p, nil
}

// scanRowWithSecret is the variant used by getProviderInternalBySlug —
// same columns plus the still-encrypted bind_password tacked on.
func scanRowWithSecret(r scannerIface) (*Provider, string, error) {
	var p Provider
	var bindPW string
	var lastTestErr sql.NullString
	var enabled, isDefault, skipVerify int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.Host, &p.Port, &p.TLSMode,
		&skipVerify, &p.BindDN, &p.UserSearchBase, &p.UserSearchFilter,
		&p.UsernameAttribute, &p.EmailAttribute,
		&p.GroupSearchBase, &p.GroupMembershipAttribute,
		&p.DefaultRole, &enabled, &isDefault,
		&lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt,
		&bindPW,
	); err != nil {
		return nil, "", err
	}
	p.SkipVerify = skipVerify == 1
	p.Enabled = enabled == 1
	p.IsDefault = isDefault == 1
	if lastTestedAt.Valid {
		t := lastTestedAt.Time
		p.LastTestedAt = &t
	}
	if lastTestOK.Valid {
		ok := lastTestOK.Int64 == 1
		p.LastTestOK = &ok
	}
	if lastTestErr.Valid {
		p.LastTestError = lastTestErr.String
	}
	p.GroupMappings = []GroupMapping{}
	return &p, bindPW, nil
}

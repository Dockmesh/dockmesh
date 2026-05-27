// Package oauth2auth implements generic OAuth 2.0 (non-OIDC) SSO
// login (§2.4). Unlike OIDC, there's no discovery and no id_token —
// we exchange the code for an access token and call the configured
// userinfo URL to learn who the user is. Common targets are GitHub,
// Bitbucket, Gitea, and any well-behaved bespoke OAuth2 provider.
package oauth2auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/secrets"

	"golang.org/x/oauth2"
)

var (
	ErrProviderNotFound = errors.New("oauth2 provider not found")
	ErrInvalidState     = errors.New("invalid state")
	ErrEmailRequired    = errors.New("provider did not return an email field")
)

type GroupMapping struct {
	GroupValue string `json:"group"`
	RoleName   string `json:"role"`
}

type Provider struct {
	ID            int64          `json:"id"`
	Slug          string         `json:"slug"`
	DisplayName   string         `json:"display_name"`
	AuthorizationURL string      `json:"authorization_url"`
	TokenURL      string         `json:"token_url"`
	UserinfoURL   string         `json:"userinfo_url"`
	ClientID      string         `json:"client_id"`
	Scopes        string         `json:"scopes"`
	UsernameField string         `json:"username_field"`
	EmailField    string         `json:"email_field"`
	GroupsField   string         `json:"groups_field"`
	DefaultRole   string         `json:"default_role"`
	GroupMappings []GroupMapping `json:"group_mappings"`
	Enabled       bool           `json:"enabled"`
	IsDefault     bool           `json:"is_default"`
	LastTestedAt  *time.Time     `json:"last_tested_at,omitempty"`
	LastTestOK    *bool          `json:"last_test_ok,omitempty"`
	LastTestError string         `json:"last_test_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type ProviderInput struct {
	Slug          string         `json:"slug"`
	DisplayName   string         `json:"display_name"`
	AuthorizationURL string      `json:"authorization_url"`
	TokenURL      string         `json:"token_url"`
	UserinfoURL   string         `json:"userinfo_url"`
	ClientID      string         `json:"client_id"`
	ClientSecret  string         `json:"client_secret"`
	Scopes        string         `json:"scopes"`
	UsernameField string         `json:"username_field"`
	EmailField    string         `json:"email_field"`
	GroupsField   string         `json:"groups_field"`
	DefaultRole   string         `json:"default_role"`
	GroupMappings []GroupMapping `json:"group_mappings,omitempty"`
	Enabled       bool           `json:"enabled"`
	IsDefault     bool           `json:"is_default,omitempty"`
}

type PublicProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type Pending struct {
	Slug  string `json:"slug"`
	State string `json:"st"`
}

type Service struct {
	db      *sql.DB
	auth    *auth.Service
	secrets *secrets.Service
	baseURL string
	mu      sync.Mutex
}

func NewService(db *sql.DB, authSvc *auth.Service, secretsSvc *secrets.Service, baseURL string) *Service {
	return &Service{db: db, auth: authSvc, secrets: secretsSvc, baseURL: baseURL}
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, display_name, authorization_url, token_url, userinfo_url,
		       client_id, scopes, username_field, email_field, groups_field,
		       default_role, enabled, is_default,
		       last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at
		FROM oauth2_providers ORDER BY id`)
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
		`SELECT slug, display_name FROM oauth2_providers WHERE enabled = 1 ORDER BY display_name`)
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
	row := s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, authorization_url, token_url, userinfo_url,
		       client_id, scopes, username_field, email_field, groups_field,
		       default_role, enabled, is_default,
		       last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at
		FROM oauth2_providers WHERE id = ?`, id)
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

func (s *Service) getProviderInternalBySlug(ctx context.Context, slug string) (*Provider, string, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, authorization_url, token_url, userinfo_url,
		       client_id, scopes, username_field, email_field, groups_field,
		       default_role, enabled, is_default,
		       last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at,
		       client_secret
		FROM oauth2_providers WHERE slug = ? AND enabled = 1`, slug)
	var clientSecret string
	p := &Provider{}
	var lastTestErr sql.NullString
	var enabled, isDefault int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := row.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.AuthorizationURL, &p.TokenURL, &p.UserinfoURL,
		&p.ClientID, &p.Scopes, &p.UsernameField, &p.EmailField, &p.GroupsField,
		&p.DefaultRole, &enabled, &isDefault,
		&lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt, &clientSecret,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrProviderNotFound
		}
		return nil, "", err
	}
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
	if err := s.loadGroupMappings(ctx, p); err != nil {
		return nil, "", err
	}
	pw, derr := s.decryptSecret(clientSecret)
	if derr != nil {
		return nil, "", fmt.Errorf("decrypt client_secret: %w", derr)
	}
	return p, pw, nil
}

func (s *Service) CreateProvider(ctx context.Context, in ProviderInput) (*Provider, error) {
	if in.Slug == "" || in.AuthorizationURL == "" || in.TokenURL == "" || in.UserinfoURL == "" ||
		in.ClientID == "" || in.ClientSecret == "" {
		return nil, errors.New("slug, authorization_url, token_url, userinfo_url, client_id, client_secret required")
	}
	in = applyDefaults(in)
	enc, err := s.encryptSecret(in.ClientSecret)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth2_providers
			(slug, display_name, authorization_url, token_url, userinfo_url,
			 client_id, client_secret, scopes,
			 username_field, email_field, groups_field,
			 default_role, enabled, is_default)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.Slug, in.DisplayName, in.AuthorizationURL, in.TokenURL, in.UserinfoURL,
		in.ClientID, enc, in.Scopes,
		in.UsernameField, in.EmailField, in.GroupsField,
		in.DefaultRole, boolInt(in.Enabled), boolInt(in.IsDefault))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a provider with that slug already exists")
		}
		return nil, fmt.Errorf("insert oauth2 provider: %w", err)
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
	if in.ClientSecret == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE oauth2_providers SET
				display_name = ?, authorization_url = ?, token_url = ?, userinfo_url = ?,
				client_id = ?, scopes = ?,
				username_field = ?, email_field = ?, groups_field = ?,
				default_role = ?, enabled = ?, is_default = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.AuthorizationURL, in.TokenURL, in.UserinfoURL,
			in.ClientID, in.Scopes,
			in.UsernameField, in.EmailField, in.GroupsField,
			in.DefaultRole, boolInt(in.Enabled), boolInt(in.IsDefault), id)
		if err != nil {
			return nil, err
		}
	} else {
		enc, err := s.encryptSecret(in.ClientSecret)
		if err != nil {
			return nil, err
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE oauth2_providers SET
				display_name = ?, authorization_url = ?, token_url = ?, userinfo_url = ?,
				client_id = ?, client_secret = ?, scopes = ?,
				username_field = ?, email_field = ?, groups_field = ?,
				default_role = ?, enabled = ?, is_default = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.AuthorizationURL, in.TokenURL, in.UserinfoURL,
			in.ClientID, enc, in.Scopes,
			in.UsernameField, in.EmailField, in.GroupsField,
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM oauth2_providers WHERE id = ?`, id)
	return err
}

func (s *Service) SetGroupMappings(ctx context.Context, providerID int64, mappings []GroupMapping) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM oauth2_provider_group_mappings WHERE provider_id = ?`, providerID); err != nil {
		return err
	}
	for _, m := range mappings {
		if m.GroupValue == "" || m.RoleName == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO oauth2_provider_group_mappings (provider_id, group_value, role_name)
			 VALUES (?, ?, ?)`, providerID, m.GroupValue, m.RoleName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) loadGroupMappings(ctx context.Context, p *Provider) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_value, role_name FROM oauth2_provider_group_mappings
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
		`UPDATE oauth2_providers SET last_tested_at = CURRENT_TIMESTAMP,
		                              last_test_ok = ?, last_test_error = ?
		   WHERE id = ?`, okInt, nullable(errMsg), providerID)
	return err
}

// TestConfig issues a GET against the userinfo URL with no auth. A
// 401/403 is the expected outcome ("auth is required" = the endpoint
// is alive). Hard failures (DNS, connection refused) bubble up so the
// admin sees what's broken.
func (s *Service) TestConfig(ctx context.Context, p *Provider) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserinfoURL, nil)
	if err != nil {
		return err
	}
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Anything that isn't an outright transport error counts as
	// "the endpoint exists" — even 4xx/5xx pass the connectivity bar.
	return nil
}

// -----------------------------------------------------------------------------
// Login flow
// -----------------------------------------------------------------------------

func (s *Service) StartLogin(ctx context.Context, slug string) (string, *Pending, error) {
	p, clientSecret, err := s.getProviderInternalBySlug(ctx, slug)
	if err != nil {
		return "", nil, err
	}
	cfg := s.buildConfig(p, clientSecret)
	state := randomToken(24)
	url := cfg.AuthCodeURL(state)
	return url, &Pending{Slug: slug, State: state}, nil
}

func (s *Service) HandleCallback(ctx context.Context, pending *Pending, code, state, userAgent, ip string) (*auth.LoginResult, error) {
	if pending == nil || state != pending.State {
		return nil, ErrInvalidState
	}
	p, clientSecret, err := s.getProviderInternalBySlug(ctx, pending.Slug)
	if err != nil {
		return nil, err
	}
	cfg := s.buildConfig(p, clientSecret)
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	claims, err := s.fetchUserinfo(ctx, p, tok)
	if err != nil {
		return nil, err
	}
	subject := stringClaim(claims, p.UsernameField)
	if subject == "" {
		subject = stringClaim(claims, "sub")
	}
	if subject == "" {
		return nil, errors.New("userinfo did not return an id claim")
	}
	email := stringClaim(claims, p.EmailField)
	if email == "" {
		return nil, ErrEmailRequired
	}
	name := stringClaim(claims, p.UsernameField)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	role := p.DefaultRole
	groups := stringSliceClaim(claims, p.GroupsField)
	for _, g := range groups {
		for _, m := range p.GroupMappings {
			if g == m.GroupValue {
				role = m.RoleName
				break
			}
		}
	}

	user, err := s.jitProvision(ctx, p, subject, name, email, role)
	if err != nil {
		return nil, err
	}
	return s.auth.StartSessionForSSO(ctx, *user, userAgent, ip)
}

func (s *Service) buildConfig(p *Provider, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  p.AuthorizationURL,
			TokenURL: p.TokenURL,
		},
		RedirectURL: s.redirectURL(p.Slug),
		Scopes:      splitScopes(p.Scopes),
	}
}

func (s *Service) fetchUserinfo(ctx context.Context, p *Provider, tok *oauth2.Token) (map[string]any, error) {
	cli := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	cli.Timeout = 10 * time.Second
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	return out, nil
}

func (s *Service) jitProvision(ctx context.Context, p *Provider, subject, name, email, role string) (*auth.User, error) {
	var userID, username string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username FROM users
		 WHERE (oauth2_provider = ? AND oauth2_subject = ?)
		    OR email = ?
		 LIMIT 1`, p.Slug, subject, email).Scan(&userID, &username)
	if err != nil {
		u, cerr := s.auth.CreateSSOUser(ctx, name, email, role, p.Slug, subject)
		if cerr != nil {
			for i := 2; i < 100; i++ {
				u, cerr = s.auth.CreateSSOUser(ctx, fmt.Sprintf("%s%d", name, i), email, role, p.Slug, subject)
				if cerr == nil {
					break
				}
			}
			if cerr != nil {
				return nil, fmt.Errorf("create sso user: %w", cerr)
			}
		}
		_, _ = s.db.ExecContext(ctx,
			`UPDATE users SET oauth2_provider = ?, oauth2_subject = ?,
			                  oidc_provider = NULL, oidc_subject = NULL
			   WHERE id = ?`, p.Slug, subject, u.ID)
		return u, nil
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE users SET oauth2_provider = ?, oauth2_subject = ?, role = ?,
		                 updated_at = CURRENT_TIMESTAMP
		   WHERE id = ?`, p.Slug, subject, role, userID)
	if err != nil {
		return nil, err
	}
	return &auth.User{ID: userID, Username: username, Email: email, Role: role}, nil
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (s *Service) redirectURL(slug string) string {
	base := strings.TrimRight(s.baseURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	return base + "/api/v1/auth/oauth2/" + slug + "/callback"
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
		return stored, nil
	}
	pt, err := s.secrets.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func applyDefaults(in ProviderInput) ProviderInput {
	if in.UsernameField == "" {
		in.UsernameField = "id"
	}
	if in.EmailField == "" {
		in.EmailField = "email"
	}
	if in.GroupsField == "" {
		in.GroupsField = "groups"
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "viewer"
	}
	return in
}

func splitScopes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func stringClaim(m map[string]any, key string) string {
	if key == "" {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%v", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case int:
		return fmt.Sprintf("%d", x)
	}
	return ""
}

func stringSliceClaim(m map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	case string:
		return []string{x}
	}
	return nil
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

func scanRow(r scannerIface) (*Provider, error) {
	var p Provider
	var lastTestErr sql.NullString
	var enabled, isDefault int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.AuthorizationURL, &p.TokenURL, &p.UserinfoURL,
		&p.ClientID, &p.Scopes, &p.UsernameField, &p.EmailField, &p.GroupsField,
		&p.DefaultRole, &enabled, &isDefault,
		&lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
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

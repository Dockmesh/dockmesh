// Package oidc implements an OpenID Connect relying-party for SSO login
// (concept §2.4). Supports:
//   - Standard OIDC discovery via /.well-known/openid-configuration
//   - Authorization Code flow with PKCE (S256)
//   - JIT user provisioning with group→role mapping
//   - Multiple providers configured in parallel
//
// Works out of the box against Azure AD, Google, Okta, Auth0, Keycloak,
// Authentik, Dex, GitLab, Zitadel — anything spec-compliant.
package oidc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/secrets"

	goidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrProviderNotFound  = errors.New("oidc provider not found")
	ErrProviderDisabled  = errors.New("oidc provider disabled")
	ErrInvalidState      = errors.New("invalid state")
	ErrEmailRequired     = errors.New("provider did not return an email claim")
)

// Provider is the public view of an OIDC provider config. Secrets are
// never returned to the API.
type Provider struct {
	ID            int64     `json:"id"`
	Slug          string    `json:"slug"`
	DisplayName   string    `json:"display_name"`
	IssuerURL     string    `json:"issuer_url"`
	ClientID      string    `json:"client_id"`
	Scopes        string    `json:"scopes"`
	GroupClaim    string    `json:"group_claim,omitempty"`
	AdminGroup    string    `json:"admin_group,omitempty"`
	OperatorGroup string    `json:"operator_group,omitempty"`
	DefaultRole   string    `json:"default_role"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ProviderInput is what a caller sends on create/update.
type ProviderInput struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	IssuerURL     string `json:"issuer_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Scopes        string `json:"scopes"`
	GroupClaim    string `json:"group_claim"`
	AdminGroup    string `json:"admin_group"`
	OperatorGroup string `json:"operator_group"`
	DefaultRole   string `json:"default_role"`
	Enabled       bool   `json:"enabled"`
}

// Service holds the DB + secrets service and caches discovered provider
// configurations (go-oidc hits the issuer's /.well-known/ on New).
type Service struct {
	db      *sql.DB
	auth    *auth.Service
	secrets *secrets.Service
	baseURL string

	mu    sync.Mutex
	cache map[int64]*cachedProvider
}

type cachedProvider struct {
	provider *goidc.Provider
	config   *Provider
	verifier *goidc.IDTokenVerifier
	fetchedAt time.Time
}

func NewService(db *sql.DB, authSvc *auth.Service, secretsSvc *secrets.Service, baseURL string) *Service {
	return &Service{
		db:      db,
		auth:    authSvc,
		secrets: secretsSvc,
		baseURL: baseURL,
		cache:   make(map[int64]*cachedProvider),
	}
}

// -----------------------------------------------------------------------------
// Provider CRUD
// -----------------------------------------------------------------------------

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, display_name, issuer_url, client_id, scopes,
		       group_claim, admin_group, operator_group, default_role,
		       enabled, created_at, updated_at
		FROM oidc_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListEnabledPublic returns only the fields the login page needs (no
// secrets, no config URLs). Used by the unauthenticated login page.
type PublicProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

func (s *Service) ListEnabledPublic(ctx context.Context) ([]PublicProvider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, display_name FROM oidc_providers WHERE enabled = 1 ORDER BY display_name`)
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

func (s *Service) CreateProvider(ctx context.Context, in ProviderInput) (*Provider, error) {
	if in.Slug == "" || in.IssuerURL == "" || in.ClientID == "" || in.ClientSecret == "" {
		return nil, errors.New("slug, issuer_url, client_id, client_secret required")
	}
	if in.Scopes == "" {
		in.Scopes = "openid,profile,email"
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "viewer"
	}
	enc, err := s.encryptSecret(in.ClientSecret)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO oidc_providers
			(slug, display_name, issuer_url, client_id, client_secret, scopes,
			 group_claim, admin_group, operator_group, default_role, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.Slug, in.DisplayName, in.IssuerURL, in.ClientID, enc, in.Scopes,
		nullable(in.GroupClaim), nullable(in.AdminGroup), nullable(in.OperatorGroup),
		in.DefaultRole, boolInt(in.Enabled))
	if err != nil {
		return nil, fmt.Errorf("insert provider: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.getProvider(ctx, id)
}

func (s *Service) UpdateProvider(ctx context.Context, id int64, in ProviderInput) (*Provider, error) {
	// Secret stays unchanged if empty on update.
	if in.ClientSecret == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE oidc_providers SET
				display_name = ?, issuer_url = ?, client_id = ?, scopes = ?,
				group_claim = ?, admin_group = ?, operator_group = ?,
				default_role = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.IssuerURL, in.ClientID, in.Scopes,
			nullable(in.GroupClaim), nullable(in.AdminGroup), nullable(in.OperatorGroup),
			in.DefaultRole, boolInt(in.Enabled), id)
		if err != nil {
			return nil, err
		}
	} else {
		enc, err := s.encryptSecret(in.ClientSecret)
		if err != nil {
			return nil, err
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE oidc_providers SET
				display_name = ?, issuer_url = ?, client_id = ?, client_secret = ?,
				scopes = ?, group_claim = ?, admin_group = ?, operator_group = ?,
				default_role = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.IssuerURL, in.ClientID, enc, in.Scopes,
			nullable(in.GroupClaim), nullable(in.AdminGroup), nullable(in.OperatorGroup),
			in.DefaultRole, boolInt(in.Enabled), id)
		if err != nil {
			return nil, err
		}
	}
	// Invalidate cache
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
	return s.getProvider(ctx, id)
}

func (s *Service) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_providers WHERE id = ?`, id)
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
	return err
}

// DiscoveryReport is what TestDiscovery returns on success.
type DiscoveryReport struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string
}

// TestDiscovery fetches /.well-known/openid-configuration from the given
// issuer and verifies the `issuer` claim matches. Called by the UI's
// "Test connection" button so admins don't configure a bad URL and only
// find out at the first real login. The goidc library already does the
// issuer-URL-equality check internally, so any mismatch (localhost vs
// public URL, trailing slash, …) surfaces as a clear error here.
func (s *Service) TestDiscovery(ctx context.Context, issuer string) (*DiscoveryReport, error) {
	p, err := goidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	var meta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, fmt.Errorf("read discovery metadata: %w", err)
	}
	return &DiscoveryReport{
		Issuer:                meta.Issuer,
		AuthorizationEndpoint: meta.AuthorizationEndpoint,
		TokenEndpoint:         meta.TokenEndpoint,
		UserinfoEndpoint:      meta.UserinfoEndpoint,
	}, nil
}

// ReloadAll flushes the entire provider cache so the next login
// re-discovers every issuer. Useful after changing provider config
// at the IdP side without touching the Dockmesh row.
func (s *Service) ReloadAll() {
	s.mu.Lock()
	s.cache = make(map[int64]*cachedProvider)
	s.mu.Unlock()
}

func (s *Service) getProvider(ctx context.Context, id int64) (*Provider, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, issuer_url, client_id, scopes,
		       group_claim, admin_group, operator_group, default_role,
		       enabled, created_at, updated_at
		FROM oidc_providers WHERE id = ?`, id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProviderNotFound
	}
	return p, err
}

func (s *Service) getProviderBySlug(ctx context.Context, slug string) (int64, string, string, string, string, error) {
	var id int64
	var issuer, clientID, clientSecret, scopes string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, issuer_url, client_id, client_secret, scopes
		FROM oidc_providers WHERE slug = ? AND enabled = 1`, slug).
		Scan(&id, &issuer, &clientID, &clientSecret, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", "", "", ErrProviderNotFound
	}
	if err != nil {
		return 0, "", "", "", "", err
	}
	plain, err := s.decryptSecret(clientSecret)
	if err != nil {
		return 0, "", "", "", "", fmt.Errorf("decrypt client_secret: %w", err)
	}
	return id, issuer, clientID, plain, scopes, nil
}

// -----------------------------------------------------------------------------
// Login flow
// -----------------------------------------------------------------------------

// StartLogin builds the redirect URL to the provider's authorization
// endpoint and returns the URL + the pending state the caller should
// persist in a short-lived cookie.
type Pending struct {
	Slug     string `json:"slug"`
	Verifier string `json:"v"`  // PKCE code_verifier
	State    string `json:"st"`
	Nonce    string `json:"n"`
}

func (s *Service) StartLogin(ctx context.Context, slug string) (string, *Pending, error) {
	id, issuer, clientID, clientSecret, scopes, err := s.getProviderBySlug(ctx, slug)
	if err != nil {
		return "", nil, err
	}

	cached, err := s.ensureProvider(ctx, id, issuer, clientID)
	if err != nil {
		return "", nil, err
	}

	verifier := oauth2.GenerateVerifier()
	state := randomToken(24)
	nonce := randomToken(16)

	cfg := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     cached.provider.Endpoint(),
		RedirectURL:  s.redirectURL(slug),
		Scopes:       splitScopes(scopes),
	}
	url := cfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		goidc.Nonce(nonce),
	)
	return url, &Pending{Slug: slug, Verifier: verifier, State: state, Nonce: nonce}, nil
}

// HandleCallback exchanges the code, verifies the id_token, finds or
// creates the local user (JIT) and returns a Dockmesh session.
func (s *Service) HandleCallback(ctx context.Context, pending *Pending, code, state, userAgent, ip string) (*auth.LoginResult, error) {
	if pending == nil || state != pending.State {
		return nil, ErrInvalidState
	}

	id, issuer, clientID, clientSecret, scopes, err := s.getProviderBySlug(ctx, pending.Slug)
	if err != nil {
		return nil, err
	}
	cached, err := s.ensureProvider(ctx, id, issuer, clientID)
	if err != nil {
		return nil, err
	}

	cfg := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     cached.provider.Endpoint(),
		RedirectURL:  s.redirectURL(pending.Slug),
		Scopes:       splitScopes(scopes),
	}
	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(pending.Verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("provider did not return id_token")
	}
	idToken, err := cached.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if idToken.Nonce != pending.Nonce {
		return nil, errors.New("id_token nonce mismatch")
	}

	// Decode all claims as a generic map — we pick what we need.
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	// JIT provision / look up user.
	user, err := s.jitProvision(ctx, cached.config, idToken.Subject, claims)
	if err != nil {
		return nil, err
	}

	// Reuse the normal session flow.
	return s.auth.StartSessionForSSO(ctx, *user, userAgent, ip)
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (s *Service) ensureProvider(ctx context.Context, id int64, issuer, clientID string) (*cachedProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[id]; ok && time.Since(c.fetchedAt) < 30*time.Minute {
		return c, nil
	}
	p, err := goidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	cfg := s.getProviderConfig(ctx, id)
	s.cache[id] = &cachedProvider{
		provider:  p,
		config:    cfg,
		verifier:  p.Verifier(&goidc.Config{ClientID: clientID}),
		fetchedAt: time.Now(),
	}
	return s.cache[id], nil
}

func (s *Service) getProviderConfig(ctx context.Context, id int64) *Provider {
	p, err := s.getProvider(ctx, id)
	if err != nil {
		return nil
	}
	return p
}

func (s *Service) jitProvision(ctx context.Context, cfg *Provider, subject string, claims map[string]any) (*auth.User, error) {
	if cfg == nil {
		return nil, errors.New("provider config missing")
	}
	email := strClaim(claims, "email")
	if email == "" {
		return nil, ErrEmailRequired
	}
	name := strClaim(claims, "preferred_username")
	if name == "" {
		name = strClaim(claims, "name")
	}
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	// Resolve role from groups.
	role := cfg.DefaultRole
	if cfg.GroupClaim != "" {
		groups := extractGroups(claims, cfg.GroupClaim)
		for _, g := range groups {
			if cfg.AdminGroup != "" && g == cfg.AdminGroup {
				role = "admin"
				break
			}
			if cfg.OperatorGroup != "" && g == cfg.OperatorGroup {
				role = "operator"
			}
		}
	}

	// Try to find existing user by (provider, subject) or by email.
	var userID, username, currentRole string
	var existingEmail sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, email, role FROM users
		WHERE (oidc_provider = ? AND oidc_subject = ?)
		   OR email = ?
		LIMIT 1`, cfg.Slug, subject, email).
		Scan(&userID, &username, &existingEmail, &currentRole)
	if errors.Is(err, sql.ErrNoRows) {
		// Create new.
		u, err := s.auth.CreateSSOUser(ctx, name, email, role, cfg.Slug, subject)
		if err != nil {
			// Retry with a suffix if the chosen username collides.
			for i := 2; i < 100; i++ {
				u, err = s.auth.CreateSSOUser(ctx, fmt.Sprintf("%s%d", name, i), email, role, cfg.Slug, subject)
				if err == nil {
					break
				}
			}
			if err != nil {
				return nil, fmt.Errorf("create sso user: %w", err)
			}
		}
		return u, nil
	}
	if err != nil {
		return nil, err
	}

	// Update role + link provider/subject on every login so group changes apply.
	_, err = s.db.ExecContext(ctx, `
		UPDATE users SET
			oidc_provider = ?, oidc_subject = ?, role = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		cfg.Slug, subject, role, userID)
	if err != nil {
		return nil, err
	}
	u := &auth.User{ID: userID, Username: username, Email: email, Role: role}
	return u, nil
}

func (s *Service) redirectURL(slug string) string {
	base := strings.TrimRight(s.baseURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	return base + "/api/v1/auth/oidc/" + slug + "/callback"
}

func (s *Service) encryptSecret(plain string) (string, error) {
	if s.secrets == nil || !s.secrets.Enabled() {
		return plain, nil
	}
	ct, err := s.secrets.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Service) decryptSecret(stored string) (string, error) {
	if s.secrets == nil || !s.secrets.Enabled() {
		return stored, nil
	}
	ct, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		// Might be legacy plaintext.
		return stored, nil
	}
	pt, err := s.secrets.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// -----------------------------------------------------------------------------
// Utility funcs
// -----------------------------------------------------------------------------

func splitScopes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func strClaim(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func extractGroups(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []any:
		var out []string
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

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
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

// scanProvider handles both *sql.Row and *sql.Rows via the Scanner interface.
type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(r scanner) (*Provider, error) {
	var p Provider
	var groupClaim, adminGroup, operatorGroup sql.NullString
	var enabled int
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.IssuerURL, &p.ClientID, &p.Scopes,
		&groupClaim, &adminGroup, &operatorGroup, &p.DefaultRole,
		&enabled, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if groupClaim.Valid {
		p.GroupClaim = groupClaim.String
	}
	if adminGroup.Valid {
		p.AdminGroup = adminGroup.String
	}
	if operatorGroup.Valid {
		p.OperatorGroup = operatorGroup.String
	}
	p.Enabled = enabled == 1
	return &p, nil
}

// Avoid unused-import complaint if json is only referenced indirectly later.
var _ = json.Marshal

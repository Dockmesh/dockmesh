// Package saml implements a SAML 2.0 Service Provider for SSO login
// (§2.4, sibling of internal/oidc). Multiple IdPs are supported in
// parallel; each row in saml_providers holds one IdP's metadata + the
// attribute mapping we use to provision local users.
//
// The XML signing/canonicalization heavy-lifting is delegated to
// crewjam/saml — rolling our own here would be a security footgun.
// We instantiate a saml.ServiceProvider per login on demand from the
// stored IdP metadata; one process-wide SP keypair is shared across
// providers so admins only register Dockmesh with their IdP once.
package saml

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

var (
	ErrProviderNotFound = errors.New("saml provider not found")
	ErrInvalidMetadata  = errors.New("invalid SAML metadata XML")
	ErrInvalidAssertion = errors.New("invalid SAML assertion")
	ErrEmailRequired    = errors.New("provider did not return an email attribute")
)

// GroupMapping is one row of saml_provider_group_mappings.
type GroupMapping struct {
	GroupValue string `json:"group"`
	RoleName   string `json:"role"`
}

// Provider is the public view of a SAML provider config. Raw metadata
// XML stays in the row but is omitted from list responses; the modal
// fetches it via GetProvider when editing.
type Provider struct {
	ID              int64          `json:"id"`
	Slug            string         `json:"slug"`
	DisplayName     string         `json:"display_name"`
	EntityID        string         `json:"entity_id"`
	SSOURL          string         `json:"sso_url"`
	SLOURL          string         `json:"slo_url,omitempty"`
	IdPMetadataXML  string         `json:"idp_metadata_xml,omitempty"`
	IdPCertPEM      string         `json:"idp_cert_pem,omitempty"`
	NameIDFormat       string         `json:"nameid_format"`
	UsernameAttribute  string         `json:"username_attribute"`
	EmailAttribute     string         `json:"email_attribute"`
	GroupsAttribute    string         `json:"groups_attribute"`
	DefaultRole     string         `json:"default_role"`
	GroupMappings   []GroupMapping `json:"group_mappings"`
	Enabled         bool           `json:"enabled"`
	IsDefault       bool           `json:"is_default"`
	LastTestedAt    *time.Time     `json:"last_tested_at,omitempty"`
	LastTestOK      *bool          `json:"last_test_ok,omitempty"`
	LastTestError   string         `json:"last_test_error,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// ProviderInput is the create/update payload.
type ProviderInput struct {
	Slug           string         `json:"slug"`
	DisplayName    string         `json:"display_name"`
	IdPMetadataXML string         `json:"idp_metadata_xml"`
	NameIDFormat       string         `json:"nameid_format"`
	UsernameAttribute  string         `json:"username_attribute"`
	EmailAttribute     string         `json:"email_attribute"`
	GroupsAttribute    string         `json:"groups_attribute"`
	DefaultRole    string         `json:"default_role"`
	GroupMappings  []GroupMapping `json:"group_mappings,omitempty"`
	Enabled        bool           `json:"enabled"`
	IsDefault      bool           `json:"is_default,omitempty"`
}

// PublicProvider is what the unauthenticated login page sees.
type PublicProvider struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type Service struct {
	db      *sql.DB
	auth    *auth.Service
	secrets *secrets.Service
	baseURL string

	mu  sync.Mutex
	key *rsa.PrivateKey
	crt *x509.Certificate
}

func NewService(db *sql.DB, authSvc *auth.Service, secretsSvc *secrets.Service, baseURL string) *Service {
	return &Service{db: db, auth: authSvc, secrets: secretsSvc, baseURL: baseURL}
}

// ListProviders returns every configured provider, including
// disabled ones. Metadata XML and IdP cert are intentionally stripped
// from list responses — they're large and the modal re-fetches via
// GetProvider when an admin opens an existing row.
func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, display_name, entity_id, sso_url, slo_url,
		       nameid_format, username_attribute, email_attribute, groups_attribute, default_role,
		       enabled, is_default, last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at
		FROM saml_providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		p, err := scanListRow(rows)
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

// ListEnabledPublic feeds the login-page button row. Excludes
// metadata + attribute mapping — the page just needs slug+label.
func (s *Service) ListEnabledPublic(ctx context.Context) ([]PublicProvider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT slug, display_name FROM saml_providers WHERE enabled = 1 ORDER BY display_name`)
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
		SELECT id, slug, display_name, entity_id, sso_url, slo_url,
		       idp_metadata_xml, idp_cert_pem,
		       nameid_format, username_attribute, email_attribute, groups_attribute, default_role,
		       enabled, is_default, last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at
		FROM saml_providers WHERE id = ?`, id)
	p, err := scanFullRow(row)
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

func (s *Service) getProviderBySlug(ctx context.Context, slug string) (*Provider, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, entity_id, sso_url, slo_url,
		       idp_metadata_xml, idp_cert_pem,
		       nameid_format, username_attribute, email_attribute, groups_attribute, default_role,
		       enabled, is_default, last_tested_at, last_test_ok, last_test_error,
		       created_at, updated_at
		FROM saml_providers WHERE slug = ? AND enabled = 1`, slug)
	p, err := scanFullRow(row)
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

func (s *Service) CreateProvider(ctx context.Context, in ProviderInput) (*Provider, error) {
	if in.Slug == "" || in.IdPMetadataXML == "" {
		return nil, errors.New("slug and idp_metadata_xml required")
	}
	meta, err := parseIdPMetadata(in.IdPMetadataXML)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidMetadata, err)
	}
	if in.NameIDFormat == "" {
		in.NameIDFormat = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"
	}
	if in.UsernameAttribute == "" {
		in.UsernameAttribute = "NameID"
	}
	if in.EmailAttribute == "" {
		in.EmailAttribute = "email"
	}
	if in.GroupsAttribute == "" {
		in.GroupsAttribute = "groups"
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "viewer"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO saml_providers
			(slug, display_name, entity_id, sso_url, slo_url,
			 idp_metadata_xml, idp_cert_pem,
			 nameid_format, username_attribute, email_attribute, groups_attribute, default_role,
			 enabled, is_default)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.Slug, in.DisplayName, meta.EntityID, meta.SSOURL, nullable(meta.SLOURL),
		in.IdPMetadataXML, meta.CertPEM,
		in.NameIDFormat, in.UsernameAttribute, in.EmailAttribute, in.GroupsAttribute, in.DefaultRole,
		boolInt(in.Enabled), boolInt(in.IsDefault))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a provider with that slug already exists")
		}
		return nil, fmt.Errorf("insert saml provider: %w", err)
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
	// Re-parse metadata only if the body supplied a non-empty value; an
	// empty string means "keep the stored XML" so admins can tweak
	// attribute mappings without re-pasting the whole blob.
	if in.IdPMetadataXML != "" {
		meta, err := parseIdPMetadata(in.IdPMetadataXML)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidMetadata, err)
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE saml_providers SET
				display_name = ?, entity_id = ?, sso_url = ?, slo_url = ?,
				idp_metadata_xml = ?, idp_cert_pem = ?,
				nameid_format = ?, username_attribute = ?, email_attribute = ?,
				groups_attribute = ?, default_role = ?,
				enabled = ?, is_default = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, meta.EntityID, meta.SSOURL, nullable(meta.SLOURL),
			in.IdPMetadataXML, meta.CertPEM,
			in.NameIDFormat, in.UsernameAttribute, in.EmailAttribute,
			in.GroupsAttribute, in.DefaultRole,
			boolInt(in.Enabled), boolInt(in.IsDefault), id)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `
			UPDATE saml_providers SET
				display_name = ?, nameid_format = ?, username_attribute = ?,
				email_attribute = ?, groups_attribute = ?,
				default_role = ?, enabled = ?, is_default = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			in.DisplayName, in.NameIDFormat, in.UsernameAttribute,
			in.EmailAttribute, in.GroupsAttribute,
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM saml_providers WHERE id = ?`, id)
	return err
}

func (s *Service) SetGroupMappings(ctx context.Context, providerID int64, mappings []GroupMapping) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM saml_provider_group_mappings WHERE provider_id = ?`, providerID); err != nil {
		return err
	}
	for _, m := range mappings {
		if m.GroupValue == "" || m.RoleName == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO saml_provider_group_mappings (provider_id, group_value, role_name)
			 VALUES (?, ?, ?)`, providerID, m.GroupValue, m.RoleName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) loadGroupMappings(ctx context.Context, p *Provider) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_value, role_name FROM saml_provider_group_mappings
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

// RecordProviderTest persists the result of a metadata-parse smoke test.
func (s *Service) RecordProviderTest(ctx context.Context, providerID int64, ok bool, errMsg string) error {
	okInt := 0
	if ok {
		okInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE saml_providers SET last_tested_at = CURRENT_TIMESTAMP,
		                            last_test_ok = ?, last_test_error = ?
		   WHERE id = ?`, okInt, nullable(errMsg), providerID)
	return err
}

// TestMetadata re-parses the stored metadata XML and reports what we
// extracted. Used by the "Test" button in the provider modal so admins
// don't only discover a malformed paste at the first real login.
type TestReport struct {
	EntityID string `json:"entity_id"`
	SSOURL   string `json:"sso_url"`
	SLOURL   string `json:"slo_url,omitempty"`
	HasCert  bool   `json:"has_cert"`
}

func (s *Service) TestMetadata(xml string) (*TestReport, error) {
	meta, err := parseIdPMetadata(xml)
	if err != nil {
		return nil, err
	}
	return &TestReport{
		EntityID: meta.EntityID,
		SSOURL:   meta.SSOURL,
		SLOURL:   meta.SLOURL,
		HasCert:  meta.CertPEM != "",
	}, nil
}

// -----------------------------------------------------------------------------
// SP keypair — generated lazily on first use, persisted in DB
// -----------------------------------------------------------------------------

func (s *Service) ensureSPKey(ctx context.Context) (*rsa.PrivateKey, *x509.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil && s.crt != nil {
		return s.key, s.crt, nil
	}
	var keyPEM, certPEM string
	err := s.db.QueryRowContext(ctx,
		`SELECT key_pem, cert_pem FROM saml_sp_keys WHERE id = 1`).
		Scan(&keyPEM, &certPEM)
	if errors.Is(err, sql.ErrNoRows) {
		k, c, kp, cp, gerr := generateSPKey()
		if gerr != nil {
			return nil, nil, gerr
		}
		if _, ierr := s.db.ExecContext(ctx,
			`INSERT INTO saml_sp_keys (id, key_pem, cert_pem) VALUES (1, ?, ?)`,
			kp, cp); ierr != nil {
			return nil, nil, ierr
		}
		s.key, s.crt = k, c
		return k, c, nil
	}
	if err != nil {
		return nil, nil, err
	}
	k, c, err := parseSPKey(keyPEM, certPEM)
	if err != nil {
		return nil, nil, err
	}
	s.key, s.crt = k, c
	return k, c, nil
}

func generateSPKey() (*rsa.PrivateKey, *x509.Certificate, string, string, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "dockmesh-saml-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, "", "", err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, "", "", err
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv),
	}))
	certPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: der,
	}))
	return priv, cert, keyPEM, certPEM, nil
}

func parseSPKey(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil {
		return nil, nil, errors.New("invalid SP key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	cb, _ := pem.Decode([]byte(certPEM))
	if cb == nil {
		return nil, nil, errors.New("invalid SP cert PEM")
	}
	crt, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return key, crt, nil
}

// -----------------------------------------------------------------------------
// Scan helpers
// -----------------------------------------------------------------------------

type scanner interface{ Scan(dest ...any) error }

func scanListRow(r scanner) (*Provider, error) {
	var p Provider
	var slo, lastTestErr sql.NullString
	var enabled, isDefault int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.EntityID, &p.SSOURL, &slo,
		&p.NameIDFormat, &p.UsernameAttribute, &p.EmailAttribute, &p.GroupsAttribute, &p.DefaultRole,
		&enabled, &isDefault, &lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if slo.Valid {
		p.SLOURL = slo.String
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

func scanFullRow(r scanner) (*Provider, error) {
	var p Provider
	var slo, lastTestErr sql.NullString
	var enabled, isDefault int
	var lastTestedAt sql.NullTime
	var lastTestOK sql.NullInt64
	if err := r.Scan(
		&p.ID, &p.Slug, &p.DisplayName, &p.EntityID, &p.SSOURL, &slo,
		&p.IdPMetadataXML, &p.IdPCertPEM,
		&p.NameIDFormat, &p.UsernameAttribute, &p.EmailAttribute, &p.GroupsAttribute, &p.DefaultRole,
		&enabled, &isDefault, &lastTestedAt, &lastTestOK, &lastTestErr,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if slo.Valid {
		p.SLOURL = slo.String
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

// keep these imports used so the package still builds when the
// caller deploys without enabling at-rest encryption.
var _ = base64.StdEncoding
var _ secrets.Service

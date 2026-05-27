package saml

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"

	crewjam "github.com/crewjam/saml"
)

// Pending is the per-request state we round-trip through the user's
// browser between StartLogin and HandleACS. Like the OIDC Pending it
// gets wrapped in a short-lived signed cookie by the HTTP handler.
type Pending struct {
	Slug      string `json:"slug"`
	RequestID string `json:"rid"`
	RelayState string `json:"rs"`
}

// idpMeta is the subset of IdP metadata we actually persist on the row
// (the full XML stays in idp_metadata_xml). Parsed once on create/
// update so a malformed paste is rejected before it can break login.
type idpMeta struct {
	EntityID string
	SSOURL   string
	SLOURL   string
	CertPEM  string
}

// parseIdPMetadata walks an EntityDescriptor and extracts the bits the
// SP needs. Crewjam handles the schema; we just pull the first
// IDPSSODescriptor (most IdPs ship exactly one) and pick the first
// HTTP-Redirect / HTTP-POST binding pair we can use.
func parseIdPMetadata(xmlStr string) (*idpMeta, error) {
	xmlStr = strings.TrimSpace(xmlStr)
	if xmlStr == "" {
		return nil, errors.New("empty metadata XML")
	}
	var ed crewjam.EntityDescriptor
	if err := xml.Unmarshal([]byte(xmlStr), &ed); err != nil {
		// Some IdPs wrap descriptors in EntitiesDescriptor; try that
		// shape before giving up.
		var multi crewjam.EntitiesDescriptor
		if err2 := xml.Unmarshal([]byte(xmlStr), &multi); err2 == nil && len(multi.EntityDescriptors) > 0 {
			ed = multi.EntityDescriptors[0]
		} else {
			return nil, fmt.Errorf("parse metadata: %w", err)
		}
	}
	if len(ed.IDPSSODescriptors) == 0 {
		return nil, errors.New("metadata contains no IDPSSODescriptor")
	}
	idp := ed.IDPSSODescriptors[0]

	var ssoURL, sloURL string
	for _, s := range idp.SingleSignOnServices {
		if s.Binding == crewjam.HTTPRedirectBinding {
			ssoURL = s.Location
			break
		}
	}
	if ssoURL == "" && len(idp.SingleSignOnServices) > 0 {
		ssoURL = idp.SingleSignOnServices[0].Location
	}
	if ssoURL == "" {
		return nil, errors.New("metadata has no SSO endpoint")
	}
	for _, s := range idp.SingleLogoutServices {
		if s.Binding == crewjam.HTTPRedirectBinding {
			sloURL = s.Location
			break
		}
	}

	// Extract the first signing cert. SAML responses signed with a
	// different cert will fail validation at ACS time.
	var certPEM string
	for _, kd := range idp.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}
		for _, x := range kd.KeyInfo.X509Data.X509Certificates {
			if x.Data == "" {
				continue
			}
			// Wrap the raw base64 in PEM headers — that's what
			// callers further down the chain expect.
			body := strings.TrimSpace(x.Data)
			certPEM = "-----BEGIN CERTIFICATE-----\n" + body + "\n-----END CERTIFICATE-----\n"
			break
		}
		if certPEM != "" {
			break
		}
	}
	if certPEM == "" {
		return nil, errors.New("metadata has no signing cert")
	}

	return &idpMeta{
		EntityID: ed.EntityID,
		SSOURL:   ssoURL,
		SLOURL:   sloURL,
		CertPEM:  certPEM,
	}, nil
}

// buildSP constructs a fresh crewjam ServiceProvider for the given DB
// row. We don't cache these — provider rows change rarely, and the
// crewjam SP is cheap to construct from already-parsed metadata.
func (s *Service) buildSP(ctx context.Context, p *Provider) (*crewjam.ServiceProvider, error) {
	key, cert, err := s.ensureSPKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("sp keypair: %w", err)
	}

	var idpEntity crewjam.EntityDescriptor
	if err := xml.Unmarshal([]byte(p.IdPMetadataXML), &idpEntity); err != nil {
		var multi crewjam.EntitiesDescriptor
		if err2 := xml.Unmarshal([]byte(p.IdPMetadataXML), &multi); err2 == nil && len(multi.EntityDescriptors) > 0 {
			idpEntity = multi.EntityDescriptors[0]
		} else {
			return nil, fmt.Errorf("idp metadata: %w", err)
		}
	}

	base := strings.TrimRight(s.baseURL, "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	metadataURL, _ := url.Parse(base + "/api/v1/auth/saml/" + p.Slug + "/metadata")
	acsURL, _ := url.Parse(base + "/api/v1/auth/saml/" + p.Slug + "/acs")
	var sloURL *url.URL
	if p.SLOURL != "" {
		u, _ := url.Parse(base + "/api/v1/auth/saml/" + p.Slug + "/slo")
		sloURL = u
	}

	sp := &crewjam.ServiceProvider{
		EntityID:    metadataURL.String(),
		Key:         key,
		Certificate: cert,
		MetadataURL: *metadataURL,
		AcsURL:      *acsURL,
		IDPMetadata: &idpEntity,
	}
	if sloURL != nil {
		sp.SloURL = *sloURL
	}
	return sp, nil
}

// SPMetadata returns the SP-side metadata XML the admin pastes into
// their IdP to register Dockmesh. Provider-scoped — each slug gets its
// own ACS URL.
func (s *Service) SPMetadata(ctx context.Context, slug string) ([]byte, error) {
	p, err := s.getProviderBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		return nil, err
	}
	md := sp.Metadata()
	return xml.MarshalIndent(md, "", "  ")
}

// StartLogin builds the URL the user's browser should be redirected
// to, plus a Pending blob the caller must persist (we round-trip it
// via a signed cookie). RelayState piggybacks the original "next"
// URL through the IdP so we can land the user back where they came
// from after the round-trip.
func (s *Service) StartLogin(ctx context.Context, slug, relayState string) (string, *Pending, error) {
	p, err := s.getProviderBySlug(ctx, slug)
	if err != nil {
		return "", nil, err
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		return "", nil, err
	}
	authReq, err := sp.MakeAuthenticationRequest(
		p.SSOURL,
		crewjam.HTTPRedirectBinding,
		crewjam.HTTPPostBinding,
	)
	if err != nil {
		return "", nil, fmt.Errorf("make authn request: %w", err)
	}
	redirectURL, err := authReq.Redirect(relayState, sp)
	if err != nil {
		return "", nil, fmt.Errorf("redirect: %w", err)
	}
	return redirectURL.String(), &Pending{
		Slug:       slug,
		RequestID:  authReq.ID,
		RelayState: relayState,
	}, nil
}

// HandleACS consumes a SAML Response posted to the assertion-consumer
// endpoint, validates it, JIT-provisions the user, and starts a
// Dockmesh session. The caller is responsible for verifying the
// signed cookie that carried the Pending payload before calling here.
func (s *Service) HandleACS(ctx context.Context, pending *Pending, r *http.Request, userAgent, ip string) (*auth.LoginResult, error) {
	if pending == nil {
		return nil, errors.New("missing pending state")
	}
	p, err := s.getProviderBySlug(ctx, pending.Slug)
	if err != nil {
		return nil, err
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse acs form: %w", err)
	}
	assertion, err := sp.ParseResponse(r, []string{pending.RequestID})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAssertion, err)
	}

	subject, attrs := extractAssertion(assertion, p)
	if subject == "" {
		return nil, errors.New("assertion has no NameID subject")
	}
	email := attrs[p.EmailAttribute]
	if email == "" {
		// Many IdPs put the email in the NameID rather than as a
		// separate attribute. Fall back when the configured email
		// attribute is empty.
		if strings.Contains(subject, "@") {
			email = subject
		}
	}
	if email == "" {
		return nil, ErrEmailRequired
	}
	name := attrs[p.UsernameAttribute]
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	role := p.DefaultRole
	if groupsRaw := attrs[p.GroupsAttribute]; groupsRaw != "" {
		// Group attribute may arrive as a single value (";"/","-
		// separated) or repeated in attribute multi-values; we
		// already coalesce multi-values into a delimited string in
		// extractAssertion.
		for _, g := range splitGroups(groupsRaw) {
			for _, m := range p.GroupMappings {
				if m.GroupValue == g {
					role = m.RoleName
					break
				}
			}
		}
	}

	user, err := s.jitProvision(ctx, p, subject, name, email, role)
	if err != nil {
		return nil, err
	}
	return s.auth.StartSessionForSSO(ctx, *user, userAgent, ip)
}

func (s *Service) jitProvision(ctx context.Context, p *Provider, subject, name, email, role string) (*auth.User, error) {
	// Try existing user by (provider, subject) or by email.
	var userID, username string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username FROM users
		 WHERE (saml_provider = ? AND saml_subject = ?)
		    OR email = ?
		 LIMIT 1`, p.Slug, subject, email).Scan(&userID, &username)
	if err != nil {
		// New user — create.
		u, cerr := s.auth.CreateSSOUser(ctx, name, email, role, p.Slug, subject)
		if cerr != nil {
			// Retry with numeric suffix if username collides.
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
		// CreateSSOUser writes the OIDC columns; backfill the SAML
		// columns separately so subsequent lookups by saml_subject
		// hit the row.
		_, _ = s.db.ExecContext(ctx,
			`UPDATE users SET saml_provider = ?, saml_subject = ?,
			                  oidc_provider = NULL, oidc_subject = NULL
			   WHERE id = ?`, p.Slug, subject, u.ID)
		return u, nil
	}
	// Existing user — refresh role + provider linkage so group
	// changes at the IdP propagate on every login.
	_, err = s.db.ExecContext(ctx, `
		UPDATE users SET saml_provider = ?, saml_subject = ?, role = ?,
		                 updated_at = CURRENT_TIMESTAMP
		   WHERE id = ?`, p.Slug, subject, role, userID)
	if err != nil {
		return nil, err
	}
	return &auth.User{ID: userID, Username: username, Email: email, Role: role}, nil
}

func extractAssertion(a *crewjam.Assertion, _ *Provider) (subject string, attrs map[string]string) {
	attrs = make(map[string]string)
	if a == nil {
		return "", attrs
	}
	if a.Subject != nil && a.Subject.NameID != nil {
		subject = a.Subject.NameID.Value
	}
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			// Pick whichever name shape the IdP used. We index on
			// both Name and FriendlyName so the admin's attribute
			// mapping can use either.
			vals := make([]string, 0, len(at.Values))
			for _, v := range at.Values {
				if v.Value != "" {
					vals = append(vals, v.Value)
				}
			}
			joined := strings.Join(vals, ";")
			if at.Name != "" {
				attrs[at.Name] = joined
			}
			if at.FriendlyName != "" && at.FriendlyName != at.Name {
				attrs[at.FriendlyName] = joined
			}
		}
	}
	return subject, attrs
}

func splitGroups(raw string) []string {
	if raw == "" {
		return nil
	}
	for _, sep := range []string{";", ",", "\n"} {
		if strings.Contains(raw, sep) {
			parts := strings.Split(raw, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if t := strings.TrimSpace(p); t != "" {
					out = append(out, t)
				}
			}
			return out
		}
	}
	return []string{strings.TrimSpace(raw)}
}

// RandomRelayState produces an opaque relay-state value the caller can
// stash in the user's session before redirecting to the IdP.
func RandomRelayState() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// keep imports used in case future paths need them
var _ = time.Now

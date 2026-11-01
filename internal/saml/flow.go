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

	"github.com/dockmesh/dockmesh/internal/rbac"

	"github.com/dockmesh/dockmesh/internal/auth"

	crewjam "github.com/crewjam/saml"
)

// Pending is the per-request state we round-trip through the user's
// browser between StartLogin and HandleACS. Like the OIDC Pending it
// gets wrapped in a short-lived signed cookie by the HTTP handler.
type Pending struct {
	Slug       string `json:"slug"`
	RequestID  string `json:"rid"`
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
		// Without it crewjam asks for a transient NameID, which changes on
		// every login and can't identify an account.
		AuthnNameIDFormat: crewjam.NameIDFormat(p.NameIDFormat),
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
	s.issueRequest(authReq.ID)
	return redirectURL.String(), &Pending{
		Slug:       slug,
		RequestID:  authReq.ID,
		RelayState: relayState,
	}, nil
}

// ACSResult is a completed SAML login. RekeyedUser is set when the login
// gave a marked account its new subject.
type ACSResult struct {
	Login       *auth.LoginResult
	RekeyedUser string
}

// HandleACS consumes a SAML Response posted to the assertion-consumer
// endpoint, validates it, JIT-provisions the user, and starts a
// Dockmesh session. The caller is responsible for verifying the
// signed cookie that carried the Pending payload before calling here.
func (s *Service) HandleACS(ctx context.Context, pending *Pending, r *http.Request, userAgent, ip string) (*ACSResult, error) {
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
	// Each request is answered once, so a captured response and cookie
	// can't be replayed.
	if !s.consumeRequest(pending.RequestID) {
		return nil, ErrRequestUsed
	}

	subject, email, name, attrs, err := identity(p, assertion)
	if err != nil {
		return nil, err
	}

	// Multi-valued group attributes are already coalesced into one
	// delimited string by extractAssertion.
	var groups []string
	if groupsRaw := attrs[p.GroupsAttribute]; groupsRaw != "" {
		groups = splitGroups(groupsRaw)
	}
	assignments := rbac.IdPAssignments("SAML "+p.DisplayName, groups, GroupRules(p.GroupMappings), p.DefaultRole, nil)

	rekeyed, err := s.rekey(ctx, p.Slug, subject, email)
	if err != nil {
		return nil, err
	}
	user, err := s.jitProvision(ctx, p, subject, name, email, assignments)
	if err != nil {
		return nil, err
	}
	login, err := s.auth.StartSessionForSSO(ctx, *user, userAgent, ip)
	if err != nil {
		return nil, err
	}
	return &ACSResult{Login: login, RekeyedUser: rekeyed}, nil
}

// identity reads who signed in: the subject that keys the account (the
// NameID, or the configured subject attribute), the email and the name.
func identity(p *Provider, a *crewjam.Assertion) (subject, email, name string, attrs map[string]string, err error) {
	nameID, attrs := extractAssertion(a, p)
	subject = nameID
	if p.SubjectAttribute != "" {
		vals := attributeValues(a, p.SubjectAttribute)
		if len(vals) != 1 {
			return "", "", "", nil, fmt.Errorf("assertion needs exactly one %q value to identify the user, got %d", p.SubjectAttribute, len(vals))
		}
		subject = vals[0]
	}
	if subject == "" {
		return "", "", "", nil, errors.New("assertion has no NameID subject")
	}
	email = attrs[p.EmailAttribute]
	if email == "" && strings.Contains(nameID, "@") {
		// Many IdPs put the email in the NameID rather than in an
		// attribute.
		email = nameID
	}
	if email == "" {
		return "", "", "", nil, ErrEmailRequired
	}
	name = attrs[p.UsernameAttribute]
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	return subject, email, name, attrs, nil
}

// rekey gives a marked account of this provider (saml_rekey_until) the new
// subject once, matched by email; one that already has it loses the mark.
// The IdP's email isn't verified, so like email linking it never takes an
// admin account or one with two-factor authentication.
func (s *Service) rekey(ctx context.Context, slug, subject, email string) (string, error) {
	var known int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE saml_provider = ? AND saml_subject = ?`, slug, subject).Scan(&known); err != nil {
		return "", err
	}
	if known > 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE users SET saml_rekey_until = NULL
			 WHERE saml_provider = ? AND saml_subject = ? AND saml_rekey_until IS NOT NULL`, slug, subject)
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM users
		 WHERE saml_provider = ? AND email = ? COLLATE NOCASE AND saml_rekey_until > CURRENT_TIMESTAMP
		   AND NOT EXISTS (SELECT 1 FROM role_assignments a JOIN role_levels l ON l.role_name = a.role_name WHERE a.user_id = users.id AND l.area IN ('users', 'settings') AND l.level = 'manage')
		   AND totp_verified = 0`, slug, email)
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
	// With several marked accounts for one email none is clearly the
	// user's; an admin has to sort them out.
	if len(ids) != 1 {
		return "", nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE users SET saml_subject = ?, saml_rekey_until = NULL, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND saml_rekey_until IS NOT NULL`, subject, ids[0])
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", nil
	}
	return ids[0], nil
}

func (s *Service) jitProvision(ctx context.Context, p *Provider, subject, name, email string, assignments []rbac.Assignment) (*auth.User, error) {
	return s.auth.ProvisionSSOUser(ctx, auth.SSOIdentity{
		Kind: "saml", Provider: p.Slug, Subject: subject,
		Email: email, EmailVerified: false,
		Username: name, Assignments: assignments,
	})
}

// requestTTL matches the lifetime of the signed state cookie.
const requestTTL = 5 * time.Minute

// maxRequests bounds the open sign-ins the unauthenticated login endpoint
// can pile up; beyond it the oldest is dropped.
const maxRequests = 10000

// issueRequest remembers an AuthnRequest ID until it is answered or
// expires.
func (s *Service) issueRequest(id string) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	now := time.Now()
	if s.requests == nil {
		s.requests = map[string]time.Time{}
	}
	if now.Sub(s.reqSwept) > time.Second || len(s.requests) >= maxRequests {
		s.reqSwept = now
		oldest, oldestExp := "", time.Time{}
		for k, exp := range s.requests {
			if now.After(exp) {
				delete(s.requests, k)
			} else if oldest == "" || exp.Before(oldestExp) {
				oldest, oldestExp = k, exp
			}
		}
		if len(s.requests) >= maxRequests {
			delete(s.requests, oldest)
		}
	}
	s.requests[id] = now.Add(requestTTL)
}

// consumeRequest reports whether id was issued, has not expired and was
// not answered yet, and marks it answered.
func (s *Service) consumeRequest(id string) bool {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	exp, ok := s.requests[id]
	delete(s.requests, id)
	return ok && time.Now().Before(exp)
}

// attributeValues returns the non-empty values of the attribute with the
// given Name or FriendlyName.
func attributeValues(a *crewjam.Assertion, name string) []string {
	var out []string
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			if at.Name != name && at.FriendlyName != name {
				continue
			}
			for _, v := range at.Values {
				if v.Value != "" {
					out = append(out, v.Value)
				}
			}
		}
	}
	return out
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

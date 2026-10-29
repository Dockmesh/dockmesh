package saml

import (
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	crewjam "github.com/crewjam/saml"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestCheckNameIDFormat(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: NameIDPersistent},
		{in: NameIDPersistent, want: NameIDPersistent},
		{in: NameIDUnspecified, want: NameIDUnspecified},
		{in: NameIDEmail, want: NameIDEmail},
		{in: "urn:oasis:names:tc:SAML:2.0:nameid-format:transient", wantErr: true},
		{in: "username", wantErr: true},
	}
	for _, tt := range tests {
		got, err := checkNameIDFormat(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("checkNameIDFormat(%q) = %q, %v; want %q, err %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func assertion(nameID string, attrs map[string]string) *crewjam.Assertion {
	a := &crewjam.Assertion{Subject: &crewjam.Subject{NameID: &crewjam.NameID{Value: nameID}}}
	st := crewjam.AttributeStatement{}
	for k, v := range attrs {
		at := crewjam.Attribute{Name: k}
		for _, part := range strings.Split(v, ";") {
			at.Values = append(at.Values, crewjam.AttributeValue{Value: part})
		}
		st.Attributes = append(st.Attributes, at)
	}
	a.AttributeStatements = []crewjam.AttributeStatement{st}
	return a
}

func TestIdentity(t *testing.T) {
	const oid = "http://schemas.microsoft.com/identity/claims/objectidentifier"
	tests := []struct {
		name        string
		subjectAttr string
		nameID      string
		attrs       map[string]string
		wantSubject string
		wantEmail   string
		wantErr     bool
	}{
		{name: "persistent NameID", nameID: "a1b2c3", attrs: map[string]string{"email": "ann@example.com"},
			wantSubject: "a1b2c3", wantEmail: "ann@example.com"},
		{name: "email only in the NameID", nameID: "ann@example.com", attrs: map[string]string{},
			wantSubject: "ann@example.com", wantEmail: "ann@example.com"},
		{name: "subject attribute wins over the NameID", subjectAttr: oid, nameID: "ann@example.com",
			attrs:       map[string]string{oid: "00000000-1111", "email": "ann.new@example.com"},
			wantSubject: "00000000-1111", wantEmail: "ann.new@example.com"},
		{name: "subject attribute missing", subjectAttr: oid, nameID: "a1b2c3",
			attrs: map[string]string{"email": "ann@example.com"}, wantErr: true},
		{name: "subject attribute with two values", subjectAttr: "uid", nameID: "a1b2c3",
			attrs: map[string]string{"uid": "a;b", "email": "ann@example.com"}, wantErr: true},
		{name: "no email anywhere", nameID: "a1b2c3", attrs: map[string]string{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{SubjectAttribute: tt.subjectAttr, EmailAttribute: "email", UsernameAttribute: "NameID"}
			subject, email, _, _, err := identity(p, assertion(tt.nameID, tt.attrs))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if subject != tt.wantSubject || email != tt.wantEmail {
				t.Errorf("got subject %q email %q, want %q %q", subject, email, tt.wantSubject, tt.wantEmail)
			}
		})
	}
}

func TestRequestIDsAreSingleUse(t *testing.T) {
	s := &Service{}
	s.issueRequest("id-1")
	if !s.consumeRequest("id-1") {
		t.Fatal("issued request not accepted")
	}
	if s.consumeRequest("id-1") {
		t.Fatal("request accepted twice")
	}
	if s.consumeRequest("never-issued") {
		t.Fatal("unknown request accepted")
	}
	s.issueRequest("id-2")
	s.requests["id-2"] = time.Now().Add(-time.Second)
	if s.consumeRequest("id-2") {
		t.Fatal("expired request accepted")
	}
	for i := 0; i < maxRequests+50; i++ {
		s.issueRequest("flood-" + strconv.Itoa(i))
	}
	if len(s.requests) > maxRequests {
		t.Fatalf("%d open requests, want at most %d", len(s.requests), maxRequests)
	}
	if !s.consumeRequest("flood-" + strconv.Itoa(maxRequests+49)) {
		t.Fatal("newest request was dropped")
	}
}

// A provider that is deleted, or pointed at another IdP, must not re-key
// the accounts marked for it.
func TestProviderChangeDropsRekeyMarkers(t *testing.T) {
	tests := []struct {
		name       string
		change     func(t *testing.T, s *Service, id int64)
		wantMarked bool
	}{
		{name: "delete", change: func(t *testing.T, s *Service, id int64) {
			if err := s.DeleteProvider(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "other IdP", change: func(t *testing.T, s *Service, id int64) {
			md := strings.Replace(testIdPMetadataWithCert(t), "https://idp.example/realm", "https://other.example/realm", 1)
			if _, err := s.UpdateProvider(context.Background(), id, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: md, Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same IdP", wantMarked: true, change: func(t *testing.T, s *Service, id int64) {
			if _, err := s.UpdateProvider(context.Background(), id, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: testIdPMetadataWithCert(t), Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			res, err := database.Exec(`INSERT INTO saml_providers (slug, display_name, entity_id, sso_url, idp_metadata_xml, idp_cert_pem, nameid_format, enabled)
				VALUES ('corp', 'Corp', 'https://idp.example/realm', 'https://idp.example/sso', ?, '', ?, 1)`, testIdPMetadata, NameIDPersistent)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			if _, err := database.Exec(`INSERT INTO users (id, username, email, password, saml_provider, saml_subject, saml_rekey_until)
				VALUES ('u1', 'ann', 'ann@example.com', 'x', 'corp', 'G-1', datetime('now','+90 days'))`); err != nil {
				t.Fatal(err)
			}
			s := &Service{db: database, auth: auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))}
			tt.change(t, s, id)
			var marked bool
			if err := database.QueryRow(`SELECT saml_rekey_until IS NOT NULL FROM users WHERE id = 'u1'`).Scan(&marked); err != nil {
				t.Fatal(err)
			}
			if marked != tt.wantMarked {
				t.Errorf("marked = %v, want %v", marked, tt.wantMarked)
			}
		})
	}
}

// testIdPMetadataWithCert is testIdPMetadata with the signing certificate
// parseIdPMetadata requires.
func testIdPMetadataWithCert(t *testing.T) string {
	t.Helper()
	_, _, _, certPEM, err := generateSPKey()
	if err != nil {
		t.Fatal(err)
	}
	block := strings.TrimSpace(certPEM)
	block = strings.TrimPrefix(block, "-----BEGIN CERTIFICATE-----")
	block = strings.TrimSuffix(block, "-----END CERTIFICATE-----")
	block = strings.ReplaceAll(block, "\n", "")
	kd := `<KeyDescriptor use="signing"><KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate>` +
		block + `</X509Certificate></X509Data></KeyInfo></KeyDescriptor>`
	return strings.Replace(testIdPMetadata, `protocol">`, `protocol">`+kd, 1)
}

func TestRekey(t *testing.T) {
	type acct struct {
		id, email string
		provider  any // nil = local account
		subject   string
		marker    string // SQL expression for saml_rekey_until, "" = NULL
		role      string
		totp      int
	}
	tests := []struct {
		name        string
		accounts    []acct
		subject     string
		email       string
		wantRekeyed string
		wantSubject map[string]string
		wantMarked  map[string]bool
	}{
		{name: "marked account of the provider",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "G-old", marker: "datetime('now','+90 days')"}},
			subject:  "persist-1", email: "ANN@example.com", wantRekeyed: "u1",
			wantSubject: map[string]string{"u1": "persist-1"}, wantMarked: map[string]bool{"u1": false}},
		{name: "marker expired",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "G-old", marker: "datetime('now','-1 day')"}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": "G-old"}},
		{name: "unmarked account",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "old"}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": "old"}},
		{name: "account of another provider",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "other", subject: "G-old", marker: "datetime('now','+90 days')"}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": "G-old"}, wantMarked: map[string]bool{"u1": true}},
		{name: "local account with the email",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: nil, marker: "datetime('now','+90 days')"}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": ""}},
		{name: "email differs",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "G-old", marker: "datetime('now','+90 days')"}},
			subject:  "persist-1", email: "bob@example.com",
			wantSubject: map[string]string{"u1": "G-old"}, wantMarked: map[string]bool{"u1": true}},
		{name: "admin account is never re-keyed",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "G-old", marker: "datetime('now','+90 days')", role: "admin"}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": "G-old"}, wantMarked: map[string]bool{"u1": true}},
		{name: "account with two-factor authentication is never re-keyed",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "G-old", marker: "datetime('now','+90 days')", totp: 1}},
			subject:  "persist-1", email: "ann@example.com",
			wantSubject: map[string]string{"u1": "G-old"}, wantMarked: map[string]bool{"u1": true}},
		{name: "known subject only drops the marker",
			accounts: []acct{{id: "u1", email: "ann@example.com", provider: "corp", subject: "persist-1", marker: "datetime('now','+90 days')"}},
			subject:  "persist-1", email: "other@example.com",
			wantSubject: map[string]string{"u1": "persist-1"}, wantMarked: map[string]bool{"u1": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			for _, a := range tt.accounts {
				marker := "NULL"
				if a.marker != "" {
					marker = a.marker
				}
				var subject any
				if a.subject != "" {
					subject = a.subject
				}
				role := a.role
				if role == "" {
					role = "viewer"
				}
				if _, err := database.Exec(`INSERT INTO users (id, username, email, password, role, totp_verified, saml_provider, saml_subject, saml_rekey_until)
					VALUES (?, ?, ?, 'x', ?, ?, ?, ?, `+marker+`)`, a.id, a.id, a.email, role, a.totp, a.provider, subject); err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{db: database}
			got, err := s.rekey(context.Background(), "corp", tt.subject, tt.email)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantRekeyed {
				t.Errorf("rekeyed %q, want %q", got, tt.wantRekeyed)
			}
			for id, want := range tt.wantSubject {
				var subject sql.NullString
				var marked bool
				if err := database.QueryRow(`SELECT saml_subject, saml_rekey_until IS NOT NULL FROM users WHERE id = ?`, id).Scan(&subject, &marked); err != nil {
					t.Fatal(err)
				}
				if subject.String != want {
					t.Errorf("%s subject = %q, want %q", id, subject.String, want)
				}
				if wantMarked, ok := tt.wantMarked[id]; ok && marked != wantMarked {
					t.Errorf("%s marked = %v, want %v", id, marked, wantMarked)
				}
			}
			if again, _ := s.rekey(context.Background(), "corp", "persist-2", tt.email); again != "" {
				t.Errorf("second re-key of %q happened", again)
			}
		})
	}
}

const testIdPMetadata = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example/realm">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

func TestStartLoginRequestsConfiguredNameID(t *testing.T) {
	tests := []struct{ format string }{{NameIDPersistent}, {NameIDEmail}}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			database := newTestDB(t)
			if _, err := database.Exec(`INSERT INTO saml_providers
				(slug, display_name, entity_id, sso_url, idp_metadata_xml, idp_cert_pem, nameid_format, enabled)
				VALUES ('corp', 'Corp', 'https://idp.example/realm', 'https://idp.example/sso', ?, '', ?, 1)`,
				testIdPMetadata, tt.format); err != nil {
				t.Fatal(err)
			}
			s := &Service{db: database, baseURL: "https://dm.example.com"}
			redirect, pending, err := s.StartLogin(context.Background(), "corp", "relay")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(redirect)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
			if err != nil {
				t.Fatal(err)
			}
			xml, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(xml), `Format="`+tt.format+`"`) {
				t.Errorf("AuthnRequest does not ask for %s:\n%s", tt.format, xml)
			}
			if !s.consumeRequest(pending.RequestID) {
				t.Error("request ID was not recorded")
			}
		})
	}
}

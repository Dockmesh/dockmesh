package saml

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// testIdP signs assertions with a key whose certificate is in the
// provider's metadata.
type testIdP struct {
	cert tls.Certificate
	pem  string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	certPEM, keyPEM := certAndKey(t, time.Now().AddDate(1, 0, 0))
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return &testIdP{cert: pair, pem: certPEM}
}

func (idp *testIdP) metadata(t *testing.T) string {
	t.Helper()
	block, _ := pem.Decode([]byte(idp.pem))
	kd := `<KeyDescriptor use="signing"><KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate>` +
		base64.StdEncoding.EncodeToString(block.Bytes) + `</X509Certificate></X509Data></KeyInfo></KeyDescriptor>`
	return strings.Replace(testIdPMetadata, `protocol">`, `protocol">`+kd, 1)
}

func (idp *testIdP) sign(t *testing.T, xmlStr string) *etree.Element {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlStr); err != nil {
		t.Fatal(err)
	}
	ctx := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(idp.cert))
	// The assertion is verified inside the Response, whose namespaces an
	// inclusive canonicalization would pull in.
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	signed, err := ctx.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

type assertionParts struct {
	issuer  bool
	subject string // the Subject children before SubjectConfirmation
	noSubj  bool
	encrypt bool // to the SP's certificate
}

func (idp *testIdP) response(t *testing.T, requestID, acs, audience string, spCert *x509.Certificate, parts assertionParts) string {
	t.Helper()
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	issuer := ""
	if parts.issuer {
		issuer = `<saml:Issuer>https://idp.example/realm</saml:Issuer>`
	}
	subject := `<saml:Subject>` + parts.subject +
		`<saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer">` +
		`<saml:SubjectConfirmationData InResponseTo="` + requestID + `" Recipient="` + acs + `" NotOnOrAfter="` + ts(5*time.Minute) + `"/>` +
		`</saml:SubjectConfirmation></saml:Subject>`
	if parts.noSubj {
		subject = ""
	}
	a := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_a1" Version="2.0" IssueInstant="` + ts(0) + `">` +
		issuer + subject +
		`<saml:Conditions NotBefore="` + ts(-time.Minute) + `" NotOnOrAfter="` + ts(5*time.Minute) + `">` +
		`<saml:AudienceRestriction><saml:Audience>` + audience + `</saml:Audience></saml:AudienceRestriction></saml:Conditions>` +
		`<saml:AttributeStatement><saml:Attribute Name="email"><saml:AttributeValue>ann@example.com</saml:AttributeValue></saml:Attribute></saml:AttributeStatement>` +
		`</saml:Assertion>`
	signed := idp.sign(t, a)

	doc := etree.NewDocument()
	if err := doc.ReadFromString(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"` +
		` ID="_r1" Version="2.0" IssueInstant="` + ts(0) + `" Destination="` + acs + `" InResponseTo="` + requestID + `">` +
		`<saml:Issuer>https://idp.example/realm</saml:Issuer>` +
		`<samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status></samlp:Response>`); err != nil {
		t.Fatal(err)
	}
	if parts.encrypt {
		plain := etree.NewDocument()
		plain.SetRoot(signed)
		buf, err := plain.WriteToBytes()
		if err != nil {
			t.Fatal(err)
		}
		enc := xmlenc.OAEP()
		enc.BlockCipher = xmlenc.AES128CBC
		enc.DigestMethod = &xmlenc.SHA1
		data, err := enc.Encrypt(spCert, buf, nil)
		if err != nil {
			t.Fatal(err)
		}
		data.CreateAttr("Type", "http://www.w3.org/2001/04/xmlenc#Element")
		wrapped := etree.NewElement("saml:EncryptedAssertion")
		wrapped.AddChild(data)
		signed = wrapped
	}
	doc.Root().AddChild(signed)
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const samlNameID = `<saml:NameID Format="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">persist-ann</saml:NameID>`

func TestHandleACSAssertionShapes(t *testing.T) {
	tests := []struct {
		name    string
		parts   assertionParts
		wantErr error
	}{
		{name: "normal assertion", parts: assertionParts{issuer: true, subject: samlNameID}},
		{name: "missing issuer", parts: assertionParts{subject: samlNameID}, wantErr: ErrInvalidAssertion},
		{name: "missing subject", parts: assertionParts{issuer: true, noSubj: true}, wantErr: ErrLoginFailed},
		{name: "foreign-namespace NameID only", wantErr: ErrInvalidAssertion,
			parts: assertionParts{issuer: true, subject: `<x:NameID xmlns:x="urn:example:other">persist-admin</x:NameID>`}},
		{name: "NameID without namespace", wantErr: ErrInvalidAssertion,
			parts: assertionParts{issuer: true, subject: `<NameID>persist-admin</NameID>`}},
		{name: "foreign NameID after the real one", wantErr: ErrInvalidAssertion,
			parts: assertionParts{issuer: true, subject: samlNameID + `<x:NameID xmlns:x="urn:example:other">persist-admin</x:NameID>`}},
		{name: "encrypted assertion", parts: assertionParts{issuer: true, subject: samlNameID, encrypt: true}},
		{name: "encrypted foreign-namespace NameID", wantErr: ErrInvalidAssertion,
			parts: assertionParts{issuer: true, encrypt: true, subject: `<x:NameID xmlns:x="urn:example:other">persist-admin</x:NameID>`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := optionsService(t)
			ctx := context.Background()
			store := rbac.NewStore(s.db)
			s.auth.SetAccessHooks(func(ctx context.Context) {
				if err := store.Load(ctx); err != nil {
					t.Error(err)
				}
			}, store.ReplaceIdPAssignments)
			idp := newTestIdP(t)
			if _, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: idp.metadata(t), Enabled: true}); err != nil {
				t.Fatal(err)
			}
			_, pending, err := s.StartLogin(ctx, "corp", "relay")
			if err != nil {
				t.Fatal(err)
			}
			_, spCert, err := s.ensureSPKey(ctx)
			if err != nil {
				t.Fatal(err)
			}
			const acs = "https://dm.example.com/api/v1/auth/saml/corp/acs"
			body := idp.response(t, pending.RequestID, acs, "https://dm.example.com/api/v1/auth/saml/corp/metadata", spCert, tt.parts)
			form := url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte(body))}, "RelayState": {"relay"}}
			r := httptest.NewRequest(http.MethodPost, acs, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			res, err := s.HandleACS(ctx, pending, r, "test", "127.0.0.1")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				var n int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE saml_provider = 'corp'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("%d accounts created from a refused response", n)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Login == nil || res.Login.User == nil {
				t.Fatal("no session started")
			}
			var subject, email string
			if err := s.db.QueryRow(`SELECT saml_subject, email FROM users WHERE id = ?`, res.Login.User.ID).Scan(&subject, &email); err != nil {
				t.Fatal(err)
			}
			if subject != "persist-ann" || email != "ann@example.com" {
				t.Errorf("account keyed by %q / %q, want persist-ann / ann@example.com", subject, email)
			}
		})
	}
}

func TestCheckNamespaces(t *testing.T) {
	const head = `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:x="urn:example:other">`
	const undecryptable = `<saml:EncryptedAssertion><xenc:EncryptedData xmlns:xenc="http://www.w3.org/2001/04/xmlenc#"/></saml:EncryptedAssertion>`
	attrs := func(inner string) string {
		return head + undecryptable + `<saml:Assertion><saml:Subject>` + samlNameID + `</saml:Subject>` + inner + `</saml:Assertion></samlp:Response>`
	}
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "qualified", raw: attrs(`<saml:AttributeStatement><saml:Attribute Name="email"><saml:AttributeValue>a@example.com</saml:AttributeValue></saml:Attribute></saml:AttributeStatement>`)},
		{name: "foreign NameID", wantErr: true,
			raw: head + `<saml:Assertion><saml:Subject><NameID xmlns="urn:example:other">x</NameID></saml:Subject></saml:Assertion></samlp:Response>`},
		{name: "foreign AttributeStatement", wantErr: true,
			raw: attrs(`<x:AttributeStatement><saml:Attribute Name="email"><saml:AttributeValue>a@example.com</saml:AttributeValue></saml:Attribute></x:AttributeStatement>`)},
		{name: "foreign Attribute", wantErr: true,
			raw: attrs(`<saml:AttributeStatement><x:Attribute Name="email"><saml:AttributeValue>a@example.com</saml:AttributeValue></x:Attribute></saml:AttributeStatement>`)},
		{name: "foreign AttributeValue", wantErr: true,
			raw: attrs(`<saml:AttributeStatement><saml:Attribute Name="email"><x:AttributeValue>a@example.com</x:AttributeValue></saml:Attribute></saml:AttributeStatement>`)},
		{name: "second root element", wantErr: true, raw: `<decoy/>` + attrs("")},
	}
	key, _, _, _, err := generateSPKey()
	if err != nil {
		t.Fatal(err)
	}
	sp := &crewjam.ServiceProvider{Key: key}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNamespaces([]byte(tt.raw), sp)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCapturingClientKeepsTheArtifactAnswer(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		tooLarge bool
		redirect bool
	}{
		{name: "small", size: 100},
		{name: "over the limit", size: maxArtifactResponse + 1, tooLarge: true},
		{name: "after a redirect", size: 100, redirect: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Repeat("x", tt.size)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.redirect && r.URL.Path != "/final" {
					w.Header().Set("Location", "/final")
					w.WriteHeader(http.StatusTemporaryRedirect)
					_, _ = w.Write([]byte("<decoy/>"))
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			captured := &capturedBody{}
			resp, err := capturingClient(srv.Client(), captured).Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			read, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(read) != body {
				t.Error("caller got a different body")
			}
			if captured.tooLarge != tt.tooLarge {
				t.Errorf("tooLarge = %v, want %v", captured.tooLarge, tt.tooLarge)
			}
			if !tt.tooLarge && captured.buf.String() != body {
				t.Error("captured body differs")
			}
		})
	}
}

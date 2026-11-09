package saml

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
)

func ptr(s string) *string { return &s }

func optionsService(t *testing.T) *Service {
	t.Helper()
	database := newTestDB(t)
	return &Service{
		db:      database,
		auth:    auth.NewService(database, []byte("0123456789abcdef0123456789abcdef")),
		baseURL: func() string { return "https://dm.example.com" },
	}
}

// metadataServer serves body over TLS and points s at its client.
func metadataServer(t *testing.T, s *Service, body *atomic.Value) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	s.metadataClient = srv.Client()
	return srv.URL + "/metadata"
}

func certAndKey(t *testing.T, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "test"}, NotBefore: notAfter.AddDate(-2, 0, 0), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return certPEM, keyPEM
}

func TestCreateProviderFromMetadataURL(t *testing.T) {
	s := optionsService(t)
	var body atomic.Value
	body.Store(testIdPMetadataWithCert(t))
	url := metadataServer(t, s, &body)
	ctx := context.Background()

	p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataURL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.IdPMetadataURL != url || p.EntityID != "https://idp.example/realm" || p.MetadataFetchedAt == nil {
		t.Fatalf("provider = %+v", p)
	}

	t.Run("pasted XML turns fetching off", func(t *testing.T) {
		got, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: testIdPMetadataWithCert(t), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if got.IdPMetadataURL != "" || got.MetadataFetchedAt != nil {
			t.Fatalf("URL kept after pasting XML: %+v", got)
		}
	})
}

func TestMetadataRefresh(t *testing.T) {
	first := testIdPMetadataWithCert(t)
	tests := []struct {
		name      string
		served    func() string
		wantCert  bool
		wantError string
	}{
		{name: "rotated cert is taken", served: func() string { return testIdPMetadataWithCert(t) }, wantCert: true},
		{name: "another entity is refused", served: func() string {
			return strings.Replace(first, "https://idp.example/realm", "https://evil.example/realm", 1)
		}, wantError: "instead of"},
		{name: "garbage is refused", served: func() string { return "<html>" }, wantError: "invalid SAML metadata"},
		{name: "no signing key is refused", served: func() string { return testIdPMetadata }, wantError: "no signing cert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := optionsService(t)
			var body atomic.Value
			body.Store(first)
			url := metadataServer(t, s, &body)
			ctx := context.Background()
			p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataURL: url, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			body.Store(tt.served())
			s.refreshMetadata(ctx)
			got, err := s.GetProvider(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantError != "" {
				if !strings.Contains(got.MetadataError, tt.wantError) {
					t.Fatalf("metadata_error = %q, want %q", got.MetadataError, tt.wantError)
				}
				if got.IdPMetadataXML != first || got.EntityID != p.EntityID {
					t.Fatalf("stored metadata changed on a failed refresh: entity %q", got.EntityID)
				}
				return
			}
			if got.MetadataError != "" || got.IdPCertPEM == p.IdPCertPEM {
				t.Fatalf("refresh not applied: err %q", got.MetadataError)
			}
		})
	}
}

func TestCheckMetadataURL(t *testing.T) {
	tests := []struct {
		raw     string
		pinned  bool
		wantErr bool
	}{
		{raw: "https://idp.example/md"},
		{raw: "http://idp.example/md", wantErr: true},
		{raw: "http://idp.example/md", pinned: true},
		{raw: "ftp://idp.example/md", pinned: true, wantErr: true},
		{raw: "https://user:pw@idp.example/md", wantErr: true},
		{raw: "https:///md", wantErr: true},
	}
	for _, tt := range tests {
		if _, err := checkMetadataURL(tt.raw, tt.pinned); (err != nil) != tt.wantErr {
			t.Errorf("checkMetadataURL(%q, %v) = %v, wantErr %v", tt.raw, tt.pinned, err, tt.wantErr)
		}
	}
}

func TestRefuseMetadataAddress(t *testing.T) {
	tests := []struct {
		addr    string
		refused bool
	}{
		{"203.0.113.9:443", false},
		{"8.8.8.8:443", false},
		{"[2001:db8::1]:443", false},
		{"127.0.0.1:2019", true},
		{"[::1]:443", true},
		{"[::ffff:127.0.0.1]:443", true},
		{"169.254.169.254:80", true},
		{"100.100.100.200:80", true},
		{"[fd00:ec2::254]:80", true},
		{"[fe80::1]:80", true},
		{"0.0.0.0:80", true},
		{"224.0.0.1:80", true},
	}
	for _, tt := range tests {
		if err := refuseMetadataAddress(tt.addr); (err != nil) != tt.refused {
			t.Errorf("refuseMetadataAddress(%s) = %v, refused %v", tt.addr, err, tt.refused)
		}
	}
}

func TestMetadataFetchSizeAndStatus(t *testing.T) {
	s := optionsService(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(make([]byte, maxMetadataBytes+1))
	}))
	defer srv.Close()
	s.metadataClient = srv.Client()
	for path, want := range map[string]string{"/missing": "404", "/huge": "larger than"} {
		if _, err := s.fetchMetadata(context.Background(), srv.URL+path, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
		}
	}
}

func TestSPEntityIDAndSigningCertAreUsed(t *testing.T) {
	s := optionsService(t)
	ctx := context.Background()
	cert, _ := certAndKey(t, time.Now().AddDate(1, 0, 0))
	p, err := s.CreateProvider(ctx, ProviderInput{
		Slug: "corp", DisplayName: "Corp", Enabled: true,
		// No signing key in the metadata: the pinned certificate stands in.
		IdPMetadataXML: testIdPMetadata,
		SPEntityID:     ptr("urn:dockmesh:corp"),
		SigningCertPEM: ptr(cert),
	})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if sp.EntityID != "urn:dockmesh:corp" {
		t.Errorf("SP entity = %q", sp.EntityID)
	}
	want, _ := certBody(cert)
	if sp.IDPCertificate == nil || *sp.IDPCertificate != want {
		t.Errorf("pinned certificate not handed to the SP: %v", sp.IDPCertificate)
	}

	t.Run("nil keeps, empty clears", func(t *testing.T) {
		kept, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp v2", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if kept.SPEntityID != "urn:dockmesh:corp" || kept.SigningCertPEM == "" {
			t.Fatalf("an API client that omits the fields wiped them: %+v", kept)
		}
		cleared, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, SPEntityID: ptr("")})
		if err != nil {
			t.Fatal(err)
		}
		sp, err := s.buildSP(ctx, cleared)
		if err != nil {
			t.Fatal(err)
		}
		if sp.EntityID != "https://dm.example.com/api/v1/auth/saml/corp/metadata" {
			t.Errorf("default SP entity = %q", sp.EntityID)
		}
	})

	t.Run("removing the only signing key is refused", func(t *testing.T) {
		if _, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, SigningCertPEM: ptr("")}); err == nil {
			t.Fatal("saved a provider with nothing to verify responses")
		}
	})
}

func TestProviderWithoutOptionsKeepsItsSPIdentity(t *testing.T) {
	s := optionsService(t)
	ctx := context.Background()
	p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: testIdPMetadataWithCert(t), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	spKey, _, err := s.ensureSPKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sp.EntityID != "https://dm.example.com/api/v1/auth/saml/corp/metadata" || sp.IDPCertificate != nil || sp.Key != spKey {
		t.Fatalf("existing provider behaves differently: entity %q, pin %v", sp.EntityID, sp.IDPCertificate)
	}
}

func TestEncryptionPair(t *testing.T) {
	future := time.Now().AddDate(1, 0, 0)
	cert, key := certAndKey(t, future)
	otherCert, _ := certAndKey(t, future)
	oldCert, oldKey := certAndKey(t, time.Now().AddDate(0, 0, -1))
	s := optionsService(t)
	ctx := context.Background()

	for name, in := range map[string]string{
		"certificate without key": cert,
		"key of another cert":     otherCert + key,
		"expired":                 oldCert + oldKey,
		"not PEM":                 "hello",
	} {
		if _, err := s.CreateProvider(ctx, ProviderInput{Slug: "bad", DisplayName: "Bad", IdPMetadataXML: testIdPMetadataWithCert(t), EncryptionCertPEM: ptr(in)}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, IdPMetadataXML: testIdPMetadataWithCert(t), EncryptionCertPEM: ptr(cert + key)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "PRIVATE KEY") {
		t.Fatal("the private key is in the API response")
	}
	sp, err := s.buildSP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	spKey, _, _ := s.ensureSPKey(ctx)
	if sp.Key == spKey || sp.Certificate == nil || sp.Certificate.Subject.CommonName != "test" {
		t.Fatalf("custom pair not used: %v", sp.Certificate)
	}

	saved, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, EncryptionCertPEM: ptr(cert)})
	if err != nil {
		t.Fatalf("saving the certificate back without its key: %v", err)
	}
	if saved.encryptionKey == "" {
		t.Fatal("stored key lost when the form sent the certificate back")
	}
	if _, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, EncryptionCertPEM: ptr(otherCert)}); err == nil {
		t.Fatal("accepted a certificate that doesn't match the stored key")
	}
	cleared, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", Enabled: true, EncryptionCertPEM: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.EncryptionCertPEM != "" || cleared.encryptionKey != "" {
		t.Fatalf("pair not cleared: %+v", cleared)
	}
}

func TestCheckSPEntityID(t *testing.T) {
	if _, err := checkSPEntityID("has space"); err == nil {
		t.Error("space accepted")
	}
	if _, err := checkSPEntityID(strings.Repeat("a", maxSPEntityIDLen+1)); err == nil {
		t.Error("over-long id accepted")
	}
	if got, err := checkSPEntityID("  urn:x  "); err != nil || got != "urn:x" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestMetadataClientIgnoresEnvProxy(t *testing.T) {
	if metadataHTTPClient(false).Transport.(*http.Transport).Proxy != nil {
		t.Fatal("an env proxy would bypass the address check")
	}
}

func TestRefreshOverHTTPKeepsSignInEndpoints(t *testing.T) {
	s := optionsService(t)
	md := testIdPMetadataWithCert(t)
	var body atomic.Value
	body.Store(md)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	s.metadataClient = srv.Client()
	cert, _ := certAndKey(t, time.Now().AddDate(1, 0, 0))
	ctx := context.Background()
	p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataURL: srv.URL, SigningCertPEM: ptr(cert), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	body.Store(strings.ReplaceAll(md, p.SSOURL, "https://phish.example/sso"))
	s.refreshMetadata(ctx)
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSOURL != p.SSOURL || !strings.Contains(got.MetadataError, "plain http") {
		t.Fatalf("sso_url %q, metadata_error %q", got.SSOURL, got.MetadataError)
	}
}

func TestRefreshDoesNotOverwriteAConcurrentSave(t *testing.T) {
	s := optionsService(t)
	var body atomic.Value
	body.Store(testIdPMetadataWithCert(t))
	url := metadataServer(t, s, &body)
	ctx := context.Background()
	p, err := s.CreateProvider(ctx, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataURL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pasted := testIdPMetadataWithCert(t)
	if _, _, err := s.UpdateProvider(ctx, p.ID, ProviderInput{Slug: "corp", DisplayName: "Corp", IdPMetadataXML: pasted, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	stale := refreshTarget{id: p.ID, slug: p.Slug, url: url, entity: p.EntityID, sso: p.SSOURL}
	if err := s.refreshProvider(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IdPMetadataXML != pasted {
		t.Fatal("a refresh that started before the save replaced the pasted metadata")
	}
}

func TestFetchErrorOmitsURLQuery(t *testing.T) {
	s := optionsService(t)
	s.metadataClient = &http.Client{Transport: failingTransport{}}
	_, err := s.fetchMetadata(context.Background(), "https://idp.example/md?token=SECRET", false)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

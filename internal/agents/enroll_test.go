package agents

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/pki"
)

func newTestService(t *testing.T) (*Service, *pki.Manager) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	mgr, err := pki.New(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return NewService(database, mgr, "http://dockmesh.test:8080", "wss://dockmesh.test:8443/connect"), mgr
}

func enrollReq(token string) EnrollRequest {
	return EnrollRequest{Token: token, Hostname: "h1", OS: "linux", Arch: "amd64", Version: "test"}
}

func TestEnrollTokenSingleUse(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second enroll: want ErrInvalidToken, got %v", err)
	}
}

func TestEnrollTokenExpiry(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	start := time.Now()
	svc.now = func() time.Time { return start }
	svc.SetEnrollTokenTTL(time.Hour)
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.TokenExpiresAt.Sub(start.UTC()); got != time.Hour {
		t.Fatalf("token_expires_at offset = %v, want 1h", got)
	}
	svc.now = func() time.Time { return start.Add(time.Hour + time.Second) }
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token: want ErrInvalidToken, got %v", err)
	}
	svc.now = func() time.Time { return start.Add(59 * time.Minute) }
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); err != nil {
		t.Fatalf("token within ttl: %v", err)
	}
}

func TestEnrollConcurrentSameToken(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.Enroll(ctx, enrollReq(res.Token)); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes != 1 {
		t.Fatalf("%d concurrent enrollments succeeded, want exactly 1", successes)
	}
}

func TestRotateEnrollTokenReenrollsSameHost(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Enroll(ctx, enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Get(ctx, first.AgentID)
	if err != nil {
		t.Fatal(err)
	}

	rot, err := svc.RotateEnrollToken(ctx, first.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if rot.Agent.ID != first.AgentID {
		t.Fatalf("rotate changed the host id: %s -> %s", first.AgentID, rot.Agent.ID)
	}
	second, err := svc.Enroll(ctx, enrollReq(rot.Token))
	if err != nil {
		t.Fatalf("re-enroll with rotated token: %v", err)
	}
	if second.AgentID != first.AgentID {
		t.Fatalf("re-enroll changed the host id: %s -> %s", first.AgentID, second.AgentID)
	}
	after, err := svc.Get(ctx, first.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CertFingerprint == before.CertFingerprint {
		t.Fatal("re-enroll kept the old client cert fingerprint")
	}
	if _, err := svc.LookupByFingerprint(ctx, before.CertFingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old cert still resolves to the host: %v", err)
	}
}

func TestRotateEnrollTokenInvalidatesPrevious(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateEnrollToken(ctx, res.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("old token after rotate: want ErrInvalidToken, got %v", err)
	}
}

func TestRotateEnrollTokenRefusesRevoked(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE agents SET status = 'revoked' WHERE id = ?`, res.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateEnrollToken(ctx, res.Agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate on revoked host: want ErrNotFound, got %v", err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("enroll on revoked host: want ErrInvalidToken, got %v", err)
	}
}

func TestCreateResultPinsCA(t *testing.T) {
	svc, mgr := newTestService(t)
	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.CAFingerprint != mgr.CAFingerprint() || len(res.CAFingerprint) != 64 {
		t.Fatalf("ca_fingerprint = %q", res.CAFingerprint)
	}
	if !strings.Contains(res.InstallHint, "ca="+res.CAFingerprint) {
		t.Fatalf("install hint lacks the CA fingerprint: %s", res.InstallHint)
	}
	if res.EnrollURL != "https://dockmesh.test:8443/enroll" {
		t.Fatalf("enroll_url = %q", res.EnrollURL)
	}
}

func TestEnrollURLFromAgentURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"wss://host:8443/connect", "https://host:8443/enroll"},
		{"wss://10.0.0.5:9443", "https://10.0.0.5:9443/enroll"},
		{"ws://lab:8443/connect?x=1", "http://lab:8443/enroll"},
		{"https://host/connect", "https://host/enroll"},
		{"ftp://host/connect", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := enrollURLFromAgentURL(c.in); got != c.want {
			t.Errorf("enrollURLFromAgentURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnrollHandler(t *testing.T) {
	svc, _ := newTestService(t)
	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	h := EnrollHandler(svc)
	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"get", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"bad json", http.MethodPost, "{", http.StatusBadRequest},
		{"wrong token", http.MethodPost, `{"token":"nope"}`, http.StatusUnauthorized},
		{"ok", http.MethodPost, `{"token":"` + res.Token + `","arch":"amd64"}`, http.StatusOK},
		{"reused", http.MethodPost, `{"token":"` + res.Token + `"}`, http.StatusUnauthorized},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, EnrollPath, strings.NewReader(c.body)))
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

// Agents enrolled before the listener switched to optional client certs
// must keep connecting with their stored cert and CA, while a cert from
// another CA is still rejected during the handshake.
func TestServerTLSConfigKeepsEnrolledAgents(t *testing.T) {
	svc, mgr := newTestService(t)
	tlsCfg, err := ServerTLSConfig(mgr)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := svc.Enroll(context.Background(), enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := tls.X509KeyPair([]byte(enrolled.ClientCert), []byte(enrolled.ClientKey))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(enrolled.CACert))

	rogue, err := pki.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rogueCertPEM, rogueKeyPEM, _, err := rogue.IssueClientCert("rogue", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rogueCert, err := tls.X509KeyPair(rogueCertPEM, rogueKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	get := func(certs []tls.Certificate) (int, error) {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, Certificates: certs, MinVersion: tls.VersionTLS12,
		}}}
		resp, err := c.Get(srv.URL + "/connect")
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := get([]tls.Certificate{agentCert}); err != nil || code != http.StatusOK {
		t.Fatalf("enrolled agent: code %d err %v", code, err)
	}
	if code, err := get(nil); err != nil || code != http.StatusUnauthorized {
		t.Fatalf("no client cert: code %d err %v, want 401", code, err)
	}
	if _, err := get([]tls.Certificate{rogueCert}); err == nil {
		t.Fatal("client cert from a foreign CA was accepted")
	}
}

func TestReenrollSeversOldSession(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(res.Token)); err != nil {
		t.Fatal(err)
	}
	closed := false
	svc.connected[res.Agent.ID] = newConnectedAgent(res.Agent.ID, "edge-1", func() { closed = true })

	rot, err := svc.RotateEnrollToken(ctx, res.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("rotating the token alone closed the live session")
	}
	if _, err := svc.Enroll(ctx, enrollReq(rot.Token)); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("re-enrollment left the old session open")
	}
}

func TestReissueAllEnrollTokens(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	enrolled, err := svc.Create(ctx, "enrolled")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(enrolled.Token)); err != nil {
		t.Fatal(err)
	}
	revoked, err := svc.Create(ctx, "revoked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE agents SET status = 'revoked' WHERE id = ?`, revoked.Agent.ID); err != nil {
		t.Fatal(err)
	}

	out, err := svc.ReissueAllEnrollTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Agent.ID != enrolled.Agent.ID {
		t.Fatalf("reissued %+v, want only the enrolled host", out)
	}
	if a, _ := svc.Get(ctx, revoked.Agent.ID); a.Status != "revoked" {
		t.Fatalf("revoked host became %q", a.Status)
	}
	again, err := svc.Enroll(ctx, enrollReq(out[0].Token))
	if err != nil {
		t.Fatalf("reissued token not redeemable: %v", err)
	}
	if again.AgentID != enrolled.Agent.ID {
		t.Fatalf("reissue changed the host id")
	}
}

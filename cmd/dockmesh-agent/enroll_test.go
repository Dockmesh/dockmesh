package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
)

// startEnrollServer runs a TLS server whose cert is issued by mgr's CA
// and answers enroll requests with mgr's CA in the payload.
func startEnrollServer(t *testing.T, mgr *pki.Manager, responseCA []byte) *httptest.Server {
	t.Helper()
	cert, err := tls.X509KeyPair(mgr.ServerCertPEM(), mgr.ServerKeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(agents.EnrollResponse{
			AgentID: "id-1", AgentName: "edge-1",
			ClientCert: "cert", ClientKey: "key", CACert: string(responseCA),
			AgentURL: "wss://127.0.0.1:8443/connect",
		})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func newPKI(t *testing.T) *pki.Manager {
	t.Helper()
	mgr, err := pki.New(t.TempDir(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func writeCAFile(t *testing.T, mgr *pki.Manager) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "enroll-ca.pem")
	if err := os.WriteFile(p, mgr.CACertPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runEnroll(t *testing.T, cfg agentConfig) (string, error) {
	t.Helper()
	dir := t.TempDir()
	err := enroll(cfg, filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"),
		filepath.Join(dir, "ca.crt"), filepath.Join(dir, "agent.url"))
	return dir, err
}

func TestEnrollWithPinnedCA(t *testing.T) {
	mgr := newPKI(t)
	srv := startEnrollServer(t, mgr, mgr.CACertPEM())
	dir, err := runEnroll(t, agentConfig{
		enrollURL:     srv.URL + "/enroll",
		token:         "tok",
		caFingerprint: strings.ToUpper(mgr.CAFingerprint()),
		enrollCAFile:  writeCAFile(t, mgr),
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil || string(got) != string(mgr.CACertPEM()) {
		t.Fatalf("persisted CA mismatch (err %v)", err)
	}
}

// A MITM presenting a cert from its own CA must be rejected even if it
// also ships that CA, because the pin names the real one.
func TestEnrollRejectsServerOutsidePinnedCA(t *testing.T) {
	real := newPKI(t)
	rogue := newPKI(t)
	srv := startEnrollServer(t, rogue, rogue.CACertPEM())

	cases := []struct {
		name string
		cfg  agentConfig
	}{
		{"pinned real CA", agentConfig{
			enrollURL: srv.URL, token: "tok",
			caFingerprint: real.CAFingerprint(), enrollCAFile: writeCAFile(t, real),
		}},
		{"rogue CA file with real fingerprint", agentConfig{
			enrollURL: srv.URL, token: "tok",
			caFingerprint: real.CAFingerprint(), enrollCAFile: writeCAFile(t, rogue),
		}},
	}
	for _, c := range cases {
		if _, err := runEnroll(t, c.cfg); err == nil {
			t.Errorf("%s: enrollment against a rogue server succeeded", c.name)
		}
	}
}

func TestEnrollRejectsUnpinnedResponseCA(t *testing.T) {
	real := newPKI(t)
	rogue := newPKI(t)
	srv := startEnrollServer(t, real, rogue.CACertPEM())
	_, err := runEnroll(t, agentConfig{
		enrollURL: srv.URL, token: "tok",
		caFingerprint: real.CAFingerprint(), enrollCAFile: writeCAFile(t, real),
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("want fingerprint mismatch, got %v", err)
	}
}

func TestEnrollClientTransportRules(t *testing.T) {
	mgr := newPKI(t)
	caFile := writeCAFile(t, mgr)
	cases := []struct {
		name    string
		cfg     agentConfig
		wantErr string
	}{
		{"http refused by default", agentConfig{enrollURL: "http://srv:8080/api/v1/agents/enroll"}, "plain http"},
		{"http allowed with flag", agentConfig{enrollURL: "http://srv:8080/api/v1/agents/enroll", insecureEnroll: true}, ""},
		{"https needs fingerprint", agentConfig{enrollURL: "https://srv:8443/enroll", insecureEnroll: true}, "DOCKMESH_CA_FINGERPRINT"},
		{"https needs CA file", agentConfig{enrollURL: "https://srv:8443/enroll", caFingerprint: mgr.CAFingerprint()}, "DOCKMESH_ENROLL_CA_FILE"},
		{"https wrong fingerprint", agentConfig{enrollURL: "https://srv:8443/enroll", caFingerprint: strings.Repeat("ab", 32), enrollCAFile: caFile}, "fingerprint mismatch"},
		{"https pinned", agentConfig{enrollURL: "https://srv:8443/enroll", caFingerprint: mgr.CAFingerprint(), enrollCAFile: caFile}, ""},
		{"unknown scheme", agentConfig{enrollURL: "ftp://srv/enroll"}, "unsupported scheme"},
	}
	for _, c := range cases {
		client, err := enrollClient(c.cfg)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		case c.wantErr == "" && client == nil:
			t.Errorf("%s: nil client", c.name)
		}
		if client != nil {
			if tr, ok := client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
				t.Errorf("%s: client skips TLS verification", c.name)
			}
		}
	}
}

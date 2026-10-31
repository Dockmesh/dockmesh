package agents

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildUpgradeReqCarriesChecksumPerArch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKMESH_BINARY_DIR", dir)
	bins := map[string][]byte{
		"amd64": []byte("amd64 agent build"),
		"arm64": []byte("arm64 agent build"),
	}
	for arch, content := range bins {
		if err := os.WriteFile(filepath.Join(dir, "dockmesh-agent-linux-"+arch), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := NewUpgradeController(&Service{publicURL: "http://srv:8080"}, nil)

	for arch, content := range bins {
		req, err := c.buildUpgradeReq(&ConnectedAgent{Hello: HelloPayload{Arch: arch}})
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		sum := sha256.Sum256(content)
		if req.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: sha256 = %s", arch, req.SHA256)
		}
		if want := "http://srv:8080/install/dockmesh-agent-linux-" + arch; req.BinaryURL != want {
			t.Errorf("%s: binary_url = %s, want %s", arch, req.BinaryURL, want)
		}
	}

	if _, err := c.buildUpgradeReq(&ConnectedAgent{Hello: HelloPayload{Arch: "../../etc"}}); err == nil {
		t.Error("unsupported arch accepted")
	}
	if err := os.Remove(filepath.Join(dir, "dockmesh-agent-linux-arm64")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.buildUpgradeReq(&ConnectedAgent{Hello: HelloPayload{Arch: "arm64"}}); !errors.Is(err, ErrNoAgentBinary) {
		t.Errorf("missing binary: want ErrNoAgentBinary, got %v", err)
	}
}

func TestUpgradeBackoffPerTarget(t *testing.T) {
	c := NewUpgradeController(&Service{}, nil)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	steps := []struct {
		name    string
		advance time.Duration
		target  string
		manual  bool
		want    bool
	}{
		{"first auto attempt", 0, "a", false, true},
		{"retry too soon", time.Minute, "a", false, false},
		{"second attempt after gap", autoUpgradeRetryGap, "a", false, true},
		{"third attempt after gap", autoUpgradeRetryGap, "a", false, true},
		{"cap reached", autoUpgradeRetryGap, "a", false, false},
		{"manual bypasses cap", 0, "a", true, true},
		{"new target resets", autoUpgradeRetryGap, "b", false, true},
	}
	for _, s := range steps {
		now = now.Add(s.advance)
		if got := c.allowAttempt("agent-1", s.target, s.manual); got != s.want {
			t.Fatalf("%s: allowAttempt = %v, want %v", s.name, got, s.want)
		}
	}
	if !c.allowAttempt("agent-2", "a", false) {
		t.Fatal("back-off leaked across agents")
	}
}

func TestBinaryHandlerRequiresEnrolledAgent(t *testing.T) {
	svc, mgr := newTestService(t)
	dir := t.TempDir()
	t.Setenv("DOCKMESH_BINARY_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "dockmesh-agent-linux-amd64"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := svc.Enroll(context.Background(), enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := parseCertForTest([]byte(enrolled.ClientCert))
	if err != nil {
		t.Fatal(err)
	}
	strangerPEM, _, _, err := mgr.IssueClientCert("stranger", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := parseCertForTest(strangerPEM)
	if err != nil {
		t.Fatal(err)
	}

	h := BinaryHandler(svc)
	cases := []struct {
		name string
		path string
		peer *x509.Certificate
		want int
	}{
		{"no cert", BinaryPath + "dockmesh-agent-linux-amd64", nil, http.StatusUnauthorized},
		{"unknown cert", BinaryPath + "dockmesh-agent-linux-amd64", stranger, http.StatusUnauthorized},
		{"enrolled", BinaryPath + "dockmesh-agent-linux-amd64", agentCert, http.StatusOK},
		{"other file", BinaryPath + "../dockmesh.db", agentCert, http.StatusNotFound},
		{"missing arch", BinaryPath + "dockmesh-agent-linux-arm64", agentCert, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.TLS = &tls.ConnectionState{}
		if c.peer != nil {
			req.TLS.PeerCertificates = []*x509.Certificate{c.peer}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func parseCertForTest(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

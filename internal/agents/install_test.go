package agents

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

func setupInstallAssets(t *testing.T) []byte {
	t.Helper()
	binDir := t.TempDir()
	bin := []byte("amd64 agent")
	if err := os.WriteFile(filepath.Join(binDir, "dockmesh-agent-linux-amd64"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKMESH_BINARY_DIR", binDir)
	t.Setenv("DOCKMESH_INSTALL_SCRIPT", filepath.Join("..", "..", "scripts", "install-agent.sh"))
	return bin
}

func TestInstallHandler(t *testing.T) {
	svc, mgr := newTestService(t)
	bin := setupInstallAssets(t)
	h := InstallHandler(svc, nil)

	tests := []struct {
		name     string
		method   string
		path     string
		want     int
		wantBody string
	}{
		{"script", http.MethodGet, "/install/agent.sh", http.StatusOK, `INSTALL_PIN="` + mgr.ServerKeyPin() + `"`},
		{"script ignores a token in the URL", http.MethodGet, "/install/agent.sh?token=" + strings.Repeat("A", 43), http.StatusOK, `TOKEN=""`},
		{"binary", http.MethodGet, "/install/dockmesh-agent-linux-amd64", http.StatusOK, string(bin)},
		{"binary head", http.MethodHead, "/install/dockmesh-agent-linux-amd64", http.StatusOK, ""},
		{"binary not bundled", http.MethodGet, "/install/dockmesh-agent-linux-arm64", http.StatusServiceUnavailable, ""},
		{"dmctl is not served here", http.MethodGet, "/install/dmctl-linux-amd64", http.StatusNotFound, ""},
		{"traversal", http.MethodGet, "/install/../agents-ca.key", http.StatusNotFound, ""},
		{"post", http.MethodPost, "/install/agent.sh", http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body lacks %q", tt.wantBody)
			}
		})
	}
}

func TestRenderInstallScriptGeneric(t *testing.T) {
	svc, mgr := newTestService(t)
	setupInstallAssets(t)
	out, err := svc.RenderInstallScript("")
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	for _, want := range []string{
		`TOKEN=""`,
		`INSTALL_URL="https://dockmesh.test:8443"`,
		`ENROLL_URL="https://dockmesh.test:8443/enroll"`,
		`CA_FINGERPRINT="` + mgr.CAFingerprint() + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if left := regexp.MustCompile(`\{\{[A-Z0-9_]+\}\}`).FindString(body); left != "" {
		t.Errorf("script still contains placeholder %s", left)
	}
	if _, err := svc.RenderInstallScript(`abc";reboot;"aaaaaaaaaaaaa`); err == nil {
		t.Error("malformed token rendered")
	}
}

func TestInstallURLFromAgentURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"wss://host:8443/connect", "https://host:8443"},
		{"wss://[2001:db8::1]:9443/connect", "https://[2001:db8::1]:9443"},
		{"wss://agents.example.com/connect", "https://agents.example.com"},
		{"ws://lab:8443/connect", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := installURLFromAgentURL(tt.in); got != tt.want {
			t.Errorf("installURLFromAgentURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInstallHandlerRateLimit(t *testing.T) {
	svc, _ := newTestService(t)
	setupInstallAssets(t)
	h := InstallHandler(svc, ratelimit.New(3, time.Minute, time.Minute))
	get := func(remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/install/agent.sh", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := get("192.0.2.1:1000"); code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, code)
		}
	}
	if code := get("192.0.2.1:2000"); code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: status %d, want 429", code)
	}
	if code := get("192.0.2.2:1000"); code != http.StatusOK {
		t.Fatalf("other address: status %d, want 200", code)
	}
}

func TestEnrollHandlerRateLimit(t *testing.T) {
	svc, _ := newTestService(t)
	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	h := EnrollHandler(svc, ratelimit.New(2, time.Minute, time.Minute))
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, EnrollPath, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"token":"` + res.Token + `","arch":"amd64"}`); rec.Code != http.StatusOK {
		t.Fatalf("valid enrollment: status %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if rec := post(`{"token":"wrong"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d", i, rec.Code)
		}
	}
	rec := post(`{"token":"wrong"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after the limit: status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// A wrong pin must stop the download before any byte of the script is
// read; curl does the same with --pinnedpubkey.
func TestInstallPinMatchesListener(t *testing.T) {
	svc, mgr := newTestService(t)
	setupInstallAssets(t)
	tlsCfg, err := ServerTLSConfig(mgr)
	if err != nil {
		t.Fatal(err)
	}
	srvURL := serveListenerTLS(t, tlsCfg, InstallHandler(svc, nil))

	fetch := func(pin string) (string, error) {
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // the pin check below replaces chain verification, as curl -k --pinnedpubkey does
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || pki.SPKIPin(cs.PeerCertificates[0]) != pin {
					return errors.New("public key does not match the pin")
				}
				return nil
			},
		}}}
		resp, err := client.Get(srvURL + InstallScriptPath)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	body, err := fetch(mgr.ServerKeyPin())
	if err != nil || !strings.Contains(body, "Dockmesh agent installer") {
		t.Fatalf("right pin: err %v", err)
	}
	if _, err := fetch("sha256//" + strings.Repeat("A", 43) + "="); err == nil {
		t.Fatal("wrong pin: request succeeded")
	}
}

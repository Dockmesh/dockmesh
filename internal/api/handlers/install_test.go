package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
)

func TestAgentInstallScript(t *testing.T) {
	dir := t.TempDir()
	mgr, err := pki.New(dir, []string{"dockmesh.test"})
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	armBin := []byte("arm64 agent")
	if err := os.WriteFile(filepath.Join(binDir, "dockmesh-agent-linux-arm64"), armBin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKMESH_BINARY_DIR", binDir)
	t.Setenv("DOCKMESH_INSTALL_SCRIPT", filepath.Join("..", "..", "..", "scripts", "install-agent.sh"))

	h := &Handlers{Agents: agents.NewService(nil, mgr, "http://dockmesh.test:8080", "wss://dockmesh.test:8443/connect")}
	token := strings.Repeat("A", 43)
	fp := mgr.CAFingerprint()

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"missing token", "", http.StatusBadRequest},
		{"shell metacharacters", "token=abc%22%3B%20reboot%3B%22aaaaaaaaaaaa", http.StatusBadRequest},
		{"stale CA", "token=" + token + "&ca=" + strings.Repeat("0", 64), http.StatusConflict},
		{"matching CA", "token=" + token + "&ca=" + strings.ToUpper(fp), http.StatusOK},
		{"no CA param", "token=" + token, http.StatusOK},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.AgentInstallScript(rec, httptest.NewRequest(http.MethodGet, "/install/agent.sh?"+c.query, nil))
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	h.AgentInstallScript(rec, httptest.NewRequest(http.MethodGet, "/install/agent.sh?token="+token, nil))
	body := rec.Body.String()
	sum := sha256.Sum256(armBin)
	for _, want := range []string{
		`TOKEN="` + token + `"`,
		`ENROLL_URL="https://dockmesh.test:8443/enroll"`,
		`CA_FINGERPRINT="` + fp + `"`,
		`BINARY_SHA256_ARM64="` + hex.EncodeToString(sum[:]) + `"`,
		`BINARY_SHA256_AMD64=""`,
		strings.TrimSpace(string(mgr.CACertPEM())),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered script lacks %q", want)
		}
	}
	if left := regexp.MustCompile(`\{\{[A-Z0-9_]+\}\}`).FindString(body); left != "" {
		t.Errorf("rendered script still contains placeholder %s", left)
	}
}

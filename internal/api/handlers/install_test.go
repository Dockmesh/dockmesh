package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
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
		`INSTALL_URL="https://dockmesh.test:8443"`,
		`INSTALL_PIN="` + mgr.ServerKeyPin() + `"`,
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

func TestAgentInstallScriptRaisesDeprecationNotice(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	mgr, err := pki.New(dir, []string{"dockmesh.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKMESH_BINARY_DIR", t.TempDir())
	t.Setenv("DOCKMESH_INSTALL_SCRIPT", filepath.Join("..", "..", "..", "scripts", "install-agent.sh"))
	svc := notices.NewService(database)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	n := newWebInstallNotice(svc)
	n.now = func() time.Time { return now }
	h := &Handlers{
		Agents:           agents.NewService(nil, mgr, "http://dockmesh.test:8080", "wss://dockmesh.test:8443/connect"),
		webInstallNotice: n,
	}
	token := strings.Repeat("B", 43)
	fetch := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		h.AgentInstallScript(rec, httptest.NewRequest(http.MethodGet, "/install/agent.sh?token="+token, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
	}
	openNotice := func() *notices.Notice {
		t.Helper()
		open, err := svc.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for i := range open {
			if open[i].Key == noticeKeyWebInstall {
				return &open[i]
			}
		}
		return nil
	}

	fetch()
	first := openNotice()
	if first == nil || !strings.Contains(first.Body, "192.0.2.1") || strings.Contains(first.Body, token) {
		t.Fatalf("notice after first use: %+v", first)
	}
	if err := svc.Resolve(context.Background(), noticeKeyWebInstall); err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	fetch()
	if openNotice() != nil {
		t.Fatal("notice raised again within a day")
	}
	now = now.Add(2 * time.Hour)
	fetch()
	if openNotice() == nil {
		t.Fatal("notice not raised again after a day of use")
	}

	// A restarted server takes the open notice's age instead of raising
	// it again at once.
	restarted := newWebInstallNotice(svc)
	if restarted.due(context.Background()) {
		t.Fatal("restart raised the notice again although it is open and recent")
	}
}

func TestAgentInstallScriptRateLimit(t *testing.T) {
	dir := t.TempDir()
	mgr, err := pki.New(dir, []string{"dockmesh.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKMESH_BINARY_DIR", t.TempDir())
	t.Setenv("DOCKMESH_INSTALL_SCRIPT", filepath.Join("..", "..", "..", "scripts", "install-agent.sh"))
	h := &Handlers{
		Agents:            agents.NewService(nil, mgr, "http://dockmesh.test:8080", "wss://dockmesh.test:8443/connect"),
		webInstallLimiter: ratelimit.New(2, time.Minute, time.Minute),
	}
	url := "/install/agent.sh?token=" + strings.Repeat("C", 43)
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		rec := httptest.NewRecorder()
		h.AgentInstallScript(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != want {
			t.Fatalf("request %d: status %d, want %d", i, rec.Code, want)
		}
	}
}

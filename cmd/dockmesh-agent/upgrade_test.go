package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestUpgradeBinary(t *testing.T) {
	newBin := []byte("new agent build")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBin)
	}))
	defer srv.Close()
	url := srv.URL + "/install/dockmesh-agent-linux-amd64"

	cases := []struct {
		name     string
		current  []byte
		req      agents.AgentUpgradeReq
		wantErr  string
		wantExec []byte
	}{
		{"verified replace", []byte("old"), agents.AgentUpgradeReq{BinaryURL: url, SHA256: sha(newBin)}, "", newBin},
		{"checksum mismatch", []byte("old"), agents.AgentUpgradeReq{BinaryURL: url, SHA256: sha([]byte("other"))}, "checksum mismatch", []byte("old")},
		{"missing checksum", []byte("old"), agents.AgentUpgradeReq{BinaryURL: url}, "no valid sha256", []byte("old")},
		{"already current", newBin, agents.AgentUpgradeReq{BinaryURL: url, SHA256: sha(newBin)}, errSameBinary.Error(), newBin},
		{"wrong arch", []byte("old"), agents.AgentUpgradeReq{BinaryURL: srv.URL + "/install/dockmesh-agent-linux-arm64", SHA256: sha(newBin)}, "expected dockmesh-agent-linux-amd64", []byte("old")},
	}
	for _, c := range cases {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dockmesh-agent")
		if err := os.WriteFile(exe, c.current, 0o755); err != nil {
			t.Fatal(err)
		}
		err := upgradeBinary(srv.Client(), c.req, exe, "amd64")
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
		got, _ := os.ReadFile(exe)
		if string(got) != string(c.wantExec) {
			t.Errorf("%s: executable content %q, want %q", c.name, got, c.wantExec)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Errorf("%s: leftover files in exe dir: %d entries", c.name, len(entries))
		}
	}
}

func TestAgentSelfUpgradeRejectsConcurrent(t *testing.T) {
	upgradeMu.Lock()
	defer upgradeMu.Unlock()
	if err := agentSelfUpgrade(agents.AgentUpgradeReq{}); !errors.Is(err, errUpgradeBusy) {
		t.Fatalf("want errUpgradeBusy, got %v", err)
	}
}

func TestCheckUpgradeURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://srv/install/dockmesh-agent-linux-amd64", true},
		{"http://10.0.0.1:8080/install/dockmesh-agent-linux-amd64", true},
		{"file:///install/dockmesh-agent-linux-amd64", false},
		{"https://srv/install/dmctl-linux-amd64", false},
		{"https://user:pw@srv/install/dockmesh-agent-linux-amd64", false},
		{"https:///install/dockmesh-agent-linux-amd64", false},
	}
	for _, c := range cases {
		if err := checkUpgradeURL(c.url, "amd64"); (err == nil) != c.ok {
			t.Errorf("checkUpgradeURL(%q) err = %v, want ok=%v", c.url, err, c.ok)
		}
	}
}

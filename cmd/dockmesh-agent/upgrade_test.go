package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

	cases := []struct {
		name     string
		current  []byte
		wantSHA  string
		wantErr  string
		wantExec []byte
	}{
		{"verified replace", []byte("old"), sha(newBin), "", newBin},
		{"checksum mismatch", []byte("old"), sha([]byte("other")), "checksum mismatch", []byte("old")},
		{"missing checksum", []byte("old"), "", "no valid sha256", []byte("old")},
		{"already current", newBin, sha(newBin), errSameBinary.Error(), newBin},
	}
	for _, c := range cases {
		dir := t.TempDir()
		exe := filepath.Join(dir, "dockmesh-agent")
		if err := os.WriteFile(exe, c.current, 0o755); err != nil {
			t.Fatal(err)
		}
		err := upgradeBinary(srv.Client(), srv.URL+"/agent-binary/dockmesh-agent-linux-amd64", c.wantSHA, exe)
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

func TestBinaryURL(t *testing.T) {
	cases := []struct {
		dial, want string
	}{
		{"wss://srv:8443/connect", "https://srv:8443/agent-binary/dockmesh-agent-linux-arm64"},
		{"ws://10.0.0.1:8443/connect?x=1", "http://10.0.0.1:8443/agent-binary/dockmesh-agent-linux-arm64"},
		{"https://srv/connect", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, err := binaryURL(c.dial, "arm64")
		if (err == nil) != (c.want != "") || got != c.want {
			t.Errorf("binaryURL(%q) = %q, %v; want %q", c.dial, got, err, c.want)
		}
	}
}

func TestOpenBinDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	tests := []struct {
		name              string
		dataMode, binMode os.FileMode
		serverFile        string
		elsewhere         bool
		wantData, wantBin os.FileMode
	}{
		{"old installer modes", 0o700, 0o750, "", false, 0o711, 0o755},
		{"custom modes kept", 0o750, 0o700, "", false, 0o750, 0o700},
		{"shared with a server", 0o700, 0o750, "dockmesh.env", false, 0o700, 0o750},
		{"binary not in bin", 0o700, 0o750, "", true, 0o700, 0o750},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "agent")
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if tt.serverFile != "" {
				if err := os.WriteFile(filepath.Join(dir, tt.serverFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			exe := filepath.Join(bin, "dockmesh-agent")
			if tt.elsewhere {
				exe = filepath.Join(t.TempDir(), "dockmesh-agent")
			}
			if err := os.WriteFile(exe, nil, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(bin, tt.binMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tt.dataMode); err != nil {
				t.Fatal(err)
			}
			openBinDirFor(dir, exe)
			for path, want := range map[string]os.FileMode{dir: tt.wantData, bin: tt.wantBin} {
				fi, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode().Perm(); got != want {
					t.Errorf("%s: mode %o, want %o", path, got, want)
				}
			}
		})
	}
}

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

func TestLegacyOpenBinDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	opened := map[string]os.FileMode{".": 0o711, "bin": 0o755, "agent.key": 0o600, "enroll-ca.pem": 0o600, "staging": 0o700, "staging/s": 0o644}
	unchanged := map[string]os.FileMode{".": 0o700, "bin": 0o750, "agent.key": 0o600, "enroll-ca.pem": 0o644, "staging": 0o755, "staging/s": 0o644}
	tests := []struct {
		name       string
		link       string // "symlink", "copy", "other" or "" for none
		serverFile string
		want       map[string]os.FileMode
	}{
		{"symlink into the data dir", "symlink", "", opened},
		{"regular copy", "copy", "", unchanged},
		{"symlink elsewhere", "other", "", unchanged},
		{"no file on the path", "", "", unchanged},
		{"shared with a server", "symlink", "dockmesh.env", unchanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent")
			exe := filepath.Join(dir, "bin", "dockmesh-agent")
			mustMkdir(t, filepath.Join(dir, "bin"))
			mustMkdir(t, filepath.Join(dir, "staging"))
			mustWrite(t, exe, 0o755)
			mustWrite(t, filepath.Join(dir, "agent.key"), 0o600)
			mustWrite(t, filepath.Join(dir, "enroll-ca.pem"), 0o644)
			mustWrite(t, filepath.Join(dir, "staging", "s"), 0o644)
			if tt.serverFile != "" {
				mustWrite(t, filepath.Join(dir, tt.serverFile), 0o600)
			}
			link := filepath.Join(root, "dockmesh-agent")
			switch tt.link {
			case "symlink":
				mustSymlink(t, exe, link)
			case "copy":
				mustWrite(t, link, 0o755)
			case "other":
				other := filepath.Join(root, "elsewhere")
				mustWrite(t, other, 0o755)
				mustSymlink(t, other, link)
			}
			for rel, mode := range map[string]os.FileMode{"staging": 0o755, "bin": 0o750, ".": 0o700} {
				if err := os.Chmod(filepath.Join(dir, rel), mode); err != nil {
					t.Fatal(err)
				}
			}

			legacyOpenBinDirFor(dir, exe, link)

			for rel, want := range tt.want {
				fi, err := os.Stat(filepath.Join(dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode().Perm(); got != want {
					t.Errorf("%s: mode %04o, want %04o", rel, got, want)
				}
			}
		})
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, nil, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

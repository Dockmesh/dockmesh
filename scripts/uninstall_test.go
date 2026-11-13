package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --dry-run only holds if every mutating command goes through run().
func TestUninstallMutationsUseRun(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	mutating := regexp.MustCompile(`^\s*(pkill|kill|rm|systemctl|userdel|groupdel|docker|launchctl|mv|chown)\b`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if mutating.MatchString(line) {
			t.Errorf("uninstall.sh:%d runs a mutating command outside run(): %s", i+1, strings.TrimSpace(line))
		}
	}
}

func uninstallHelpers(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "#  Env file helpers")
	end := strings.Index(s, "#  Detect install layout")
	if start < 0 || end < start {
		t.Fatal("env file helpers block not found in uninstall.sh")
	}
	return s[start:end]
}

func TestUninstallEnvFileIsNotExecuted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := uninstallHelpers(t)
	root := t.TempDir()
	assets := filepath.Join(root, "share")
	if err := os.MkdirAll(filepath.Join(assets, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(assets, "install-agent.sh"), "#!/bin/sh\n")
	noMarker := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(noMarker, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	pwned := filepath.Join(root, "pwned")
	a, n, p := filepath.ToSlash(assets), filepath.ToSlash(noMarker), filepath.ToSlash(pwned)

	tests := []struct {
		name    string
		env     string
		wantDir string
	}{
		{"plain values", "DOCKMESH_INSTALL_SCRIPT=" + a + "/install-agent.sh\nDOCKMESH_BINARY_DIR=" + a + "/bin\n", a},
		{"quoted and exported", "export DOCKMESH_BINARY_DIR=\"" + a + "/bin\"\n", a},
		{"command substitution", "X=$(touch " + p + ")\nY=`touch " + p + "`\ntouch " + p + "\nDOCKMESH_BINARY_DIR=" + a + "/bin\n", a},
		{"substitution in the key", "DOCKMESH_BINARY_DIR=$(touch " + p + ")/bin\n", ""},
		{"directory without installer", "DOCKMESH_BINARY_DIR=" + n + "/bin\n", ""},
		{"relative path", "DOCKMESH_BINARY_DIR=share/bin\n", ""},
		{"dot-dot path", "DOCKMESH_BINARY_DIR=" + a + "/../share/bin\n", ""},
		{"root", "DOCKMESH_BINARY_DIR=/bin\n", ""},
		{"no keys", "DOCKMESH_PORT=8080\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := filepath.Join(t.TempDir(), "dockmesh.env")
			mustWrite(t, env, tt.env)
			script := "set -euo pipefail\n" + helpers + "\nTRUSTED_ASSET_UID=\"$(id -u)\"\nasset_dir_from_env \"$1\"\n"
			cmd := exec.Command("bash", "-c", script, "uninstall-test", filepath.ToSlash(env))
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("asset_dir_from_env: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.wantDir {
				t.Errorf("asset dir = %q, want %q", got, tt.wantDir)
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Fatal("env file content was executed")
			}
		})
	}
}

func TestUninstallNeverSourcesEnvFile(t *testing.T) {
	b, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := regexp.MustCompile(`(^|[;&|]\s*|\s)(\.|source)\s+["$]`)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if source.MatchString(line) {
			t.Errorf("uninstall.sh:%d sources a file: %s", i+1, strings.TrimSpace(line))
		}
	}
}

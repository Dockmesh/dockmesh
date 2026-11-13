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

func runUninstallHelper(t *testing.T, dir, call string, args ...string) (string, error) {
	t.Helper()
	script := "set -euo pipefail\n" + uninstallHelpers(t) + "\n" + call + " \"$@\"\n"
	for i := range args {
		args[i] = filepath.ToSlash(args[i])
	}
	cmd := exec.Command("bash", append([]string{"-c", script, "uninstall-test"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestUninstallEnvFileIsNotExecuted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	pwned := filepath.Join(root, "pwned")
	p := filepath.ToSlash(pwned)

	tests := []struct {
		name    string
		env     string
		wantDir string
	}{
		{"plain values", "DOCKMESH_INSTALL_SCRIPT=/opt/share/dockmesh/install-agent.sh\nDOCKMESH_BINARY_DIR=/opt/share/dockmesh/bin\n", "/opt/share/dockmesh"},
		{"quoted and exported", "export DOCKMESH_BINARY_DIR=\"/opt/x/bin\"\n", "/opt/x"},
		{"installer only", "DOCKMESH_INSTALL_SCRIPT='/opt/y/install-agent.sh'\n", "/opt/y"},
		{"command substitution", "X=$(touch " + p + ")\nY=`touch " + p + "`\ntouch " + p + "\nDOCKMESH_BINARY_DIR=/opt/z/bin\n", "/opt/z"},
		{"substitution in the value", "DOCKMESH_BINARY_DIR=$(touch " + p + ")/bin\n", ""},
		{"relative path", "DOCKMESH_BINARY_DIR=share/bin\n", ""},
		{"option-like value", "DOCKMESH_BINARY_DIR=--help\n", ""},
		{"no keys", "DOCKMESH_PORT=8080\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := filepath.Join(t.TempDir(), "dockmesh.env")
			mustWrite(t, env, tt.env)
			got, err := runUninstallHelper(t, root, "asset_dir_from_env", env)
			if err != nil {
				t.Fatalf("asset_dir_from_env: %v\n%s", err, got)
			}
			if got != tt.wantDir {
				t.Errorf("asset dir = %q, want %q", got, tt.wantDir)
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Fatal("env file content was executed")
			}
		})
	}
}

func TestUninstallUsableAssetDir(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	assets := filepath.Join(root, "share", "dockmesh")
	inData := filepath.Join(dataDir, "share")
	for _, d := range []string{filepath.Join(assets, "bin"), filepath.Join(inData, "bin"), filepath.Join(root, "empty")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dataDir, "link")
	if err := os.Symlink(filepath.Join(root, "share"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"asset dir", assets, true},
		{"trailing slash", assets + "/", true},
		{"root", "/", false},
		{"relative", "share/dockmesh", false},
		{"dot-dot", filepath.Join(root, "share") + "/x/../dockmesh", false},
		{"symlinked parent", filepath.Join(link, "dockmesh"), false},
		{"inside the data dir", inData, false},
		{"not an asset dir", filepath.Join(root, "empty"), false},
		{"missing", filepath.Join(root, "missing"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runUninstallHelper(t, root, "usable_asset_dir", tt.dir, dataDir)
			if got := err == nil; got != tt.want {
				t.Errorf("usable_asset_dir(%s) = %v, want %v (%s)", tt.dir, got, tt.want, out)
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

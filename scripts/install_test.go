package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// envHelpers returns the env-file helper functions from install.sh, so they
// can be run without executing the installer itself.
func envHelpers(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	s := string(b)
	start := strings.Index(s, "#  Env file helpers")
	end := strings.Index(s, "#  Banner")
	if start < 0 || end < start {
		t.Fatal("env file helpers block not found in install.sh")
	}
	return s[start:end]
}

func runWriteEnvFile(t *testing.T, helpers, envFile, dataDir string) {
	t.Helper()
	script := "set -euo pipefail\nUSE_SUDO=\nok() { :; }\ninfo() { :; }\n" + helpers +
		"\nwrite_env_file \"$1\" \"$2\" /opt/share/dockmesh\n"
	cmd := exec.Command("bash", "-c", script, "install-test", envFile, dataDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write_env_file: %v\n%s", err, out)
	}
}

func checkMode(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Errorf("stat %s: %v", path, err)
		return
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", path, fi.Mode().Perm())
	}
}

func TestInstallWriteEnvFile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name      string
		oldEnv    string // existing env file content, "" for none
		dbInData  bool   // database already at <data>/data/dockmesh.db
		oldDBElse bool   // existing env file points at a database elsewhere that exists
		bakExists bool
		wantFlag  bool
	}{
		{name: "fresh install", wantFlag: true},
		{name: "reinstall over kept data", oldEnv: "DOCKMESH_SETUP_FORCE=true\n", dbInData: true},
		{name: "database elsewhere per old env file", oldDBElse: true},
		{name: "old env file without database", oldEnv: "DOCKMESH_HTTP_ADDR=:9090\n", wantFlag: true},
		{name: "backup already exists", oldEnv: "DOCKMESH_HTTP_ADDR=:9090\n", dbInData: true, bakExists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "dm")
			if err := os.MkdirAll(filepath.Join(dataDir, "data"), 0o750); err != nil {
				t.Fatal(err)
			}
			envFile := filepath.Join(dataDir, "dockmesh.env")
			oldEnv := tt.oldEnv
			if tt.oldDBElse {
				elsewhere := filepath.Join(root, "old.db")
				if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				oldEnv = "export DOCKMESH_DB_PATH=\"" + filepath.ToSlash(elsewhere) + "\"\n"
			}
			if oldEnv != "" {
				if err := os.WriteFile(envFile, []byte(oldEnv), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.dbInData {
				if err := os.WriteFile(filepath.Join(dataDir, "data", "dockmesh.db"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			const firstBak = "first backup\n"
			if tt.bakExists {
				if err := os.WriteFile(envFile+".bak", []byte(firstBak), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			runWriteEnvFile(t, helpers, filepath.ToSlash(envFile), filepath.ToSlash(dataDir))

			got, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatalf("read env file: %v", err)
			}
			if hasFlag := strings.Contains(string(got), "DOCKMESH_SETUP_FORCE=true"); hasFlag != tt.wantFlag {
				t.Errorf("flag written = %v, want %v\n%s", hasFlag, tt.wantFlag, got)
			}
			if !strings.Contains(string(got), "DOCKMESH_DB_PATH=") {
				t.Errorf("env file incomplete:\n%s", got)
			}
			if runtime.GOOS != "windows" {
				checkMode(t, envFile)
			}

			baks, _ := filepath.Glob(envFile + ".bak*")
			switch {
			case oldEnv == "" && len(baks) != 0:
				t.Errorf("backup written without an old env file: %v", baks)
			case oldEnv != "" && !tt.bakExists && len(baks) != 1:
				t.Errorf("backups = %v, want exactly %s.bak", baks, envFile)
			case tt.bakExists && len(baks) != 2:
				t.Errorf("backups = %v, want the old .bak plus a timestamped one", baks)
			}
			if tt.bakExists {
				if b, _ := os.ReadFile(envFile + ".bak"); string(b) != firstBak {
					t.Errorf("existing .bak was overwritten: %q", b)
				}
			}
			for _, bak := range baks {
				if tt.bakExists && bak == envFile+".bak" {
					continue
				}
				if b, _ := os.ReadFile(bak); string(b) != oldEnv {
					t.Errorf("%s = %q, want the old env file %q", bak, b, oldEnv)
				}
				if runtime.GOOS != "windows" {
					checkMode(t, bak)
				}
			}
		})
	}
}

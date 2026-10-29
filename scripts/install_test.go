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

type envRun struct {
	envFile, dataDir string
	defaultEnv       string // replaces the script's default env file locations
	systemdUnit      bool
}

func runWriteEnvFile(t *testing.T, helpers string, r envRun) error {
	t.Helper()
	unit := "0"
	if r.systemdUnit {
		unit = "1"
	}
	script := "set -euo pipefail\nUSE_SUDO=\nok() { :; }\ninfo() { :; }\ndie() { echo \"$*\" >&2; exit 1; }\n" +
		helpers +
		"\nENV_FILE_CANDIDATES=(\"$3\")\nHAS_SYSTEMD_UNIT=$4\nwrite_env_file \"$1\" \"$2\" /opt/share/dockmesh\n"
	cmd := exec.Command("bash", "-c", script, "install-test",
		filepath.ToSlash(r.envFile), filepath.ToSlash(r.dataDir), filepath.ToSlash(r.defaultEnv), unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("write_env_file output:\n%s", out)
	}
	return err
}

func checkMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Errorf("stat %s: %v", path, err)
		return
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", path, fi.Mode().Perm())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallWriteEnvFile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name        string
		oldEnv      string // existing env file content, "" for none
		dbInData    bool   // database already at <data>/data/dockmesh.db
		defaultEnv  bool   // an env file exists at a default location
		systemdUnit bool
		bakExists   bool
		wantFlag    bool
	}{
		{name: "fresh install", wantFlag: true},
		{name: "reinstall over kept data", oldEnv: "DOCKMESH_SETUP_FORCE=true\n", dbInData: true},
		{name: "database without env file", dbInData: true},
		{name: "old env file without database", oldEnv: "DOCKMESH_HTTP_ADDR=:9090\n"},
		{name: "env file at a default location", defaultEnv: true},
		{name: "systemd unit present", systemdUnit: true},
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
			defaultEnv := filepath.Join(root, "default", "dockmesh.env")
			if tt.defaultEnv {
				if err := os.MkdirAll(filepath.Dir(defaultEnv), 0o750); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, defaultEnv, "DOCKMESH_HTTP_ADDR=:8080\n")
			}
			if tt.oldEnv != "" {
				mustWrite(t, envFile, tt.oldEnv)
			}
			if tt.dbInData {
				mustWrite(t, filepath.Join(dataDir, "data", "dockmesh.db"), "")
			}
			const firstBak = "first backup\n"
			if tt.bakExists {
				mustWrite(t, envFile+".bak", firstBak)
			}

			if err := runWriteEnvFile(t, helpers, envRun{envFile: envFile, dataDir: dataDir, defaultEnv: defaultEnv, systemdUnit: tt.systemdUnit}); err != nil {
				t.Fatalf("write_env_file: %v", err)
			}

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
			checkMode(t, envFile)
			if left, _ := filepath.Glob(envFile + ".new.*"); len(left) != 0 {
				t.Errorf("temp files left behind: %v", left)
			}

			baks, _ := filepath.Glob(envFile + ".bak*")
			switch {
			case tt.oldEnv == "" && len(baks) != 0:
				t.Errorf("backup written without an old env file: %v", baks)
			case tt.oldEnv != "" && !tt.bakExists && len(baks) != 1:
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
				if b, _ := os.ReadFile(bak); string(b) != tt.oldEnv {
					t.Errorf("%s = %q, want the old env file %q", bak, b, tt.oldEnv)
				}
				checkMode(t, bak)
			}
		})
	}
}

func TestInstallWriteEnvFileRefusesSymlinks(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name    string
		linkEnv bool // env file is a symlink to the target
		linkBak bool // env file is regular, .bak is a symlink to the target
	}{
		{name: "symlinked env file", linkEnv: true},
		{name: "symlinked backup", linkBak: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "dm")
			if err := os.MkdirAll(filepath.Join(dataDir, "data"), 0o750); err != nil {
				t.Fatal(err)
			}
			envFile := filepath.Join(dataDir, "dockmesh.env")
			target := filepath.Join(root, "victim")
			const victim = "do not touch\n"
			mustWrite(t, target, victim)
			const oldEnv = "DOCKMESH_HTTP_ADDR=:9090\n"
			if tt.linkEnv {
				if err := os.Symlink(target, envFile); err != nil {
					t.Fatal(err)
				}
			}
			if tt.linkBak {
				mustWrite(t, envFile, oldEnv)
				if err := os.Symlink(target, envFile+".bak"); err != nil {
					t.Fatal(err)
				}
			}

			err := runWriteEnvFile(t, helpers, envRun{envFile: envFile, dataDir: dataDir, defaultEnv: filepath.Join(root, "none")})
			if err == nil {
				t.Fatal("write_env_file followed a symlink instead of refusing")
			}
			if b, _ := os.ReadFile(target); string(b) != victim {
				t.Errorf("symlink target changed: %q", b)
			}
			if tt.linkBak {
				if b, _ := os.ReadFile(envFile); string(b) != oldEnv {
					t.Errorf("env file changed although the run was refused: %q", b)
				}
			}
		})
	}
}

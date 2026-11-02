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

func TestInstallServiceHelpers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name string
		call string
		fake string // body of the fake systemctl
		want bool
	}{
		{"unit loaded", "systemd_unit_loaded", `echo loaded`, true},
		{"unit not found", "systemd_unit_loaded", `echo not-found`, false},
		{"systemctl fails", "systemd_unit_loaded", `exit 1`, false},
		{"new pid", "service_runs_new_binary 100", `echo 200`, true},
		{"same pid", "service_runs_new_binary 100", `echo 100`, false},
		{"no main pid", "service_runs_new_binary 100", `echo 0`, false},
		{"old pid empty", "service_runs_new_binary ''", `echo 200`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			mustWrite(t, filepath.Join(bin, "systemctl"), "#!/bin/sh\n"+tt.fake+"\n")
			if err := os.Chmod(filepath.Join(bin, "systemctl"), 0o755); err != nil {
				t.Fatal(err)
			}
			script := "set -euo pipefail\n" + helpers + "\n" + tt.call + "\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+filepath.ToSlash(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := cmd.Run()
			if got := err == nil; got != tt.want {
				t.Errorf("%s = %v, want %v (err %v)", tt.call, got, tt.want, err)
			}
		})
	}
}

func TestInstallAgentBinaries(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name  string
		files []string
		want  []string
	}{
		{"linux tarball ships both", []string{"dockmesh-agent-linux-amd64", "dockmesh-agent-linux-arm64"},
			[]string{"dockmesh-agent-linux-amd64", "dockmesh-agent-linux-arm64"}},
		{"single agent of an older release", []string{"dockmesh-agent"}, []string{"dockmesh-agent-linux-amd64"}},
		{"no agent", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, assets := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(assets, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				mustWrite(t, filepath.Join(src, f), f)
			}
			script := "set -euo pipefail\nUSE_SUDO=\nok() { :; }\n" + helpers +
				"\ninstall_agent_binaries \"$1\" \"$2\" linux amd64\n"
			cmd := exec.Command("bash", "-c", script, "t", filepath.ToSlash(src), filepath.ToSlash(assets))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			entries, err := os.ReadDir(filepath.Join(assets, "bin"))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("installed %v, want %v", got, tt.want)
			}
		})
	}
}

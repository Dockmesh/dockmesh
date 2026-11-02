package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
		wantFlag    bool
	}{
		{name: "fresh install", wantFlag: true},
		{name: "reinstall over kept data", oldEnv: "DOCKMESH_HTTP_ADDR=:9090\nDOCKMESH_BASE_URL=https://dm.lan\n", dbInData: true},
		{name: "kept env file with setup flag", oldEnv: "DOCKMESH_SETUP_FORCE=true\n", dbInData: true},
		{name: "database without env file", dbInData: true},
		{name: "old env file without database", oldEnv: "DOCKMESH_HTTP_ADDR=:9090\n"},
		{name: "env file at a default location", defaultEnv: true},
		{name: "systemd unit present", systemdUnit: true},
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

			if err := runWriteEnvFile(t, helpers, envRun{envFile: envFile, dataDir: dataDir, defaultEnv: defaultEnv, systemdUnit: tt.systemdUnit}); err != nil {
				t.Fatalf("write_env_file: %v", err)
			}

			got, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatalf("read env file: %v", err)
			}
			if baks, _ := filepath.Glob(envFile + ".bak*"); len(baks) != 0 {
				t.Errorf("backups written: %v", baks)
			}
			if left, _ := filepath.Glob(envFile + ".new.*"); len(left) != 0 {
				t.Errorf("temp files left behind: %v", left)
			}
			if tt.oldEnv != "" {
				if string(got) != tt.oldEnv {
					t.Errorf("existing env file rewritten:\n%s", got)
				}
				return
			}
			if hasFlag := strings.Contains(string(got), "DOCKMESH_SETUP_FORCE=true"); hasFlag != tt.wantFlag {
				t.Errorf("flag written = %v, want %v\n%s", hasFlag, tt.wantFlag, got)
			}
			if !strings.Contains(string(got), "DOCKMESH_DB_PATH=") {
				t.Errorf("env file incomplete:\n%s", got)
			}
			checkMode(t, envFile)
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

	root := t.TempDir()
	dataDir := filepath.Join(root, "dm")
	if err := os.MkdirAll(filepath.Join(dataDir, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dataDir, "dockmesh.env")
	target := filepath.Join(root, "victim")
	const victim = "do not touch\n"
	mustWrite(t, target, victim)
	if err := os.Symlink(target, envFile); err != nil {
		t.Fatal(err)
	}
	err := runWriteEnvFile(t, helpers, envRun{envFile: envFile, dataDir: dataDir, defaultEnv: filepath.Join(root, "none")})
	if err == nil {
		t.Fatal("write_env_file followed a symlink instead of refusing")
	}
	if b, _ := os.ReadFile(target); string(b) != victim {
		t.Errorf("symlink target changed: %q", b)
	}
}

// runHelper runs one install.sh helper with ENV_FILE_CANDIDATES set to
// candidates and returns stdout and stderr.
func runHelper(t *testing.T, helpers string, candidates []string, call string, args ...string) (string, string, error) {
	t.Helper()
	var quoted []string
	for _, c := range candidates {
		quoted = append(quoted, "'"+filepath.ToSlash(c)+"'")
	}
	script := "set -euo pipefail\nUSE_SUDO=\nok() { :; }\ninfo() { :; }\nwarn() { echo \"WARN $*\" >&2; }\ndie() { echo \"$*\" >&2; exit 1; }\n" +
		helpers + "\nENV_FILE_CANDIDATES=(" + strings.Join(quoted, " ") + ")\n" +
		"TRUSTED_ENV_OWNERS=\"${TEST_TRUSTED_OWNERS:-root dockmesh $(id -un)}\"\n" + call + " \"$@\"\n"
	for i := range args {
		args[i] = filepath.ToSlash(args[i])
	}
	cmd := exec.Command("bash", append([]string{"-c", script, "install-test"}, args...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestInstallFindExistingEnvFile(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)
	root := t.TempDir()
	explicit := filepath.Join(root, "explicit", "dockmesh.env")
	unit := filepath.Join(root, "unit", "dockmesh.env")
	defA := filepath.Join(root, "var-lib", "dockmesh.env")
	defB := filepath.Join(root, "data", "dockmesh.env")
	all := []string{explicit, unit, defA, defB}
	for _, p := range all {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		present []string
		args    []string
		want    string
	}{
		{"nothing", nil, []string{"", ""}, ""},
		{"explicit data dir first", []string{explicit, unit, defA}, []string{explicit, unit}, explicit},
		{"systemd unit before defaults", []string{unit, defA}, []string{"", unit}, unit},
		{"second default location", []string{defB}, []string{"", ""}, defB},
		{"directory is not an env file", nil, []string{filepath.Dir(defA), ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, p := range all {
				_ = os.Remove(p)
			}
			for _, p := range tt.present {
				mustWrite(t, p, "DOCKMESH_HTTP_ADDR=:8080\n")
			}
			out, stderr, err := runHelper(t, helpers, []string{defA, defB}, "find_existing_env_file", tt.args...)
			if tt.want == "" {
				if err == nil {
					t.Errorf("found %q, want none", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("find_existing_env_file: %v\n%s", err, stderr)
			}
			if out != filepath.ToSlash(tt.want) {
				t.Errorf("found %q, want %q", out, filepath.ToSlash(tt.want))
			}
		})
	}
}

func TestInstallWarnStaleAssetPaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)
	root := t.TempDir()
	oldAssets := filepath.Join(root, "old-share")
	newAssets := filepath.Join(root, "new-share")
	if err := os.MkdirAll(filepath.Join(oldAssets, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(oldAssets, "install-agent.sh"), "#!/bin/sh\n")

	tests := []struct {
		name     string
		assetDir string // the asset dir the env file names
		wantWarn bool
	}{
		{"same asset dir", newAssets, false},
		{"old asset dir still there", oldAssets, false},
		{"old asset dir gone", filepath.Join(root, "gone"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := filepath.Join(t.TempDir(), "dockmesh.env")
			a := filepath.ToSlash(tt.assetDir)
			content := "DOCKMESH_INSTALL_SCRIPT=" + a + "/install-agent.sh\nDOCKMESH_BINARY_DIR=" + a + "/bin\n"
			mustWrite(t, env, content)
			_, stderr, err := runHelper(t, helpers, nil, "warn_stale_asset_paths", env, newAssets)
			if err != nil {
				t.Fatalf("warn_stale_asset_paths: %v\n%s", err, stderr)
			}
			if got := strings.Contains(stderr, "WARN"); got != tt.wantWarn {
				t.Errorf("warned = %v, want %v: %s", got, tt.wantWarn, stderr)
			}
			if tt.wantWarn && !strings.Contains(stderr, "DOCKMESH_BINARY_DIR="+filepath.ToSlash(newAssets)+"/bin") {
				t.Errorf("warning does not name the new path: %s", stderr)
			}
			if b, _ := os.ReadFile(env); string(b) != content {
				t.Errorf("env file changed: %q", b)
			}
		})
	}
}

func TestInstallEnvFileTrust(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)

	tests := []struct {
		name      string
		file      string
		owners    string // TRUSTED_ENV_OWNERS override, "" keeps the test user trusted
		groupMode bool   // make the file group-writable
		wantOK    bool
	}{
		{name: "own dockmesh.env", file: "dockmesh.env", wantOK: true},
		{name: "other file name", file: "other.env"},
		{name: "owner not trusted", file: "dockmesh.env", owners: "root dockmesh"},
		{name: "group writable", file: "dockmesh.env", groupMode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.groupMode && runtime.GOOS == "windows" {
				t.Skip("no POSIX modes on Windows")
			}
			dir := filepath.Join(t.TempDir(), "dm")
			if err := os.Mkdir(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(dir, tt.file)
			mustWrite(t, f, "DOCKMESH_HTTP_ADDR=:8080\n")
			if tt.groupMode {
				if err := os.Chmod(f, 0o664); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TEST_TRUSTED_OWNERS", tt.owners)
			_, stderr, err := runHelper(t, helpers, nil, "env_file_trusted", f)
			if (err == nil) != tt.wantOK {
				t.Errorf("env_file_trusted = %v, want ok=%v (%s)", err, tt.wantOK, stderr)
			}
			if tt.wantOK {
				return
			}
			_, _, err = runHelper(t, helpers, []string{f}, "find_existing_env_file")
			if err == nil {
				t.Error("find_existing_env_file reused an untrusted env file")
			}
		})
	}
}

func TestInstallDataDirAllowed(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	helpers := envHelpers(t)
	tests := []struct {
		dir string
		ok  bool
	}{
		{"/var/lib/dockmesh", true},
		{"/data", true},
		{"/srv/dockmesh", true},
		{"/etc", false},
		{"/etc/", false},
		{"/", false},
		{"/usr/local", false},
		{"/var/lib", false},
		{"/home", false},
	}
	for _, tt := range tests {
		_, _, err := runHelper(t, helpers, nil, "data_dir_allowed", tt.dir)
		if (err == nil) != tt.ok {
			t.Errorf("data_dir_allowed(%s) = %v, want ok=%v", tt.dir, err, tt.ok)
		}
	}
}

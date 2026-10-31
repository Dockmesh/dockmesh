package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// agentScriptFunc returns the source of a shell function in install-agent.sh,
// delimited by "name() {" and "# end name".
func agentScriptFunc(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	b, err := os.ReadFile("install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, name+"() {")
	end := strings.Index(s, "# end "+name)
	if start < 0 || end < start {
		t.Fatalf("%s not found in install-agent.sh", name)
	}
	return s[start:end]
}

func TestInstallAgentLogState(t *testing.T) {
	fn := agentScriptFunc(t, "agent_log_state")

	tests := []struct {
		name, logs, want string
	}{
		{"empty", "", "waiting"},
		{"enrolling", `{"level":"INFO","msg":"enrolling","url":"https://s:8443"}`, "waiting"},
		{"enrolled and connected", `{"msg":"enrolled successfully"}` + "\n" + `{"msg":"connected","agent_id":"a"}`, "connected"},
		{"enrollment rejected", `{"msg":"enrolling"}` + "\n" + `{"level":"ERROR","msg":"enrollment failed","err":"token expired"}`, "failed"},
		{"tls error after enroll", `{"msg":"enrolled successfully"}` + "\n" + `{"msg":"disconnected, reconnecting","err":"x509"}`, "waiting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nagent_log_state \"$1\"", "t", tt.logs)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallAgentIsServerDataDir(t *testing.T) {
	fn := agentScriptFunc(t, "is_server_data_dir")

	withDB := t.TempDir()
	if err := os.WriteFile(filepath.Join(withDB, "dockmesh.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	withEnv := t.TempDir()
	if err := os.WriteFile(filepath.Join(withEnv, "dockmesh.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, dir string
		want      bool
	}{
		{"server default", "/var/lib/dockmesh", true},
		{"server default trailing slash", "/var/lib/dockmesh/", true},
		{"docker volume", "/data", true},
		{"agent default", "/var/lib/dockmesh-agent", false},
		{"contains database", withDB, true},
		{"contains server env", withEnv, true},
		{"unrelated", t.TempDir(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.ToSlash(tt.dir)
			err := exec.Command("bash", "-c", fn+"\nis_server_data_dir \"$1\"", "t", dir).Run()
			if got := err == nil; got != tt.want {
				t.Errorf("is_server_data_dir(%q) = %v, want %v", dir, got, tt.want)
			}
		})
	}
}

func TestInstallAgentMigrateFiles(t *testing.T) {
	fn := agentScriptFunc(t, "migrate_agent_files")

	from := t.TempDir()
	to := t.TempDir()
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(from, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agentFiles := []string{"agent.crt", "agent.key", "agent.url", "ca.crt", "enroll-ca.pem", "certs/x.pem", "staging/s", "bin/dockmesh-agent"}
	serverFiles := []string{"dockmesh.db", "dockmesh.env", "agents-ca.crt", "stacks/web/compose.yaml"}
	for _, f := range append(agentFiles, serverFiles...) {
		write(f)
	}

	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nmigrate_agent_files \"$1\" \"$2\"", "t", filepath.ToSlash(from), filepath.ToSlash(to))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	for _, f := range agentFiles {
		if _, err := os.Stat(filepath.Join(to, f)); err != nil {
			t.Errorf("%s not moved: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(from, f)); !os.IsNotExist(err) {
			t.Errorf("%s still in old dir", f)
		}
	}
	for _, f := range serverFiles {
		if _, err := os.Stat(filepath.Join(from, f)); err != nil {
			t.Errorf("server file %s touched: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(to, f)); !os.IsNotExist(err) {
			t.Errorf("server file %s copied to agent dir", f)
		}
	}
	if _, err := os.Stat(filepath.Join(from, "bin")); !os.IsNotExist(err) {
		t.Error("empty bin dir left behind")
	}
}

func TestInstallAgentPathCopy(t *testing.T) {
	fn := agentScriptFunc(t, "install_path_copy")
	if os.Geteuid() != 0 {
		t.Skip("needs root to switch to the service user and chown the copy")
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip("setpriv not available")
	}
	tests := []struct {
		name     string
		existing string // "", "symlink" or "file"
		src      string // "binary" or "secret": a symlink to a root-only file
		wantErr  bool
	}{
		{"fresh copy", "", "binary", false},
		{"replaces a symlink", "symlink", "binary", false},
		{"replaces an older copy", "file", "binary", false},
		{"source swapped for a root-only file", "file", "secret", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, d := range []string{filepath.Dir(dir), dir} {
				if err := os.Chmod(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(dir, "bin", "dockmesh-agent")
			dest := filepath.Join(dir, "path", "dockmesh-agent")
			for _, d := range []string{filepath.Dir(binary), filepath.Dir(dest)} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(binary, []byte("new"), 0o755); err != nil {
				t.Fatal(err)
			}
			src := binary
			if tt.src == "secret" {
				secret := filepath.Join(dir, "secret")
				if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				src = filepath.Join(dir, "bin", "swapped")
				if err := os.Symlink(secret, src); err != nil {
					t.Fatal(err)
				}
			}
			switch tt.existing {
			case "symlink":
				if err := os.Symlink(binary, dest); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\ninstall_path_copy \"$@\"", "t", src, dest, "nobody", "65534")
			out, err := cmd.CombinedOutput()
			if tt.wantErr {
				if err == nil {
					t.Fatal("copy succeeded, want an error")
				}
				if b, _ := os.ReadFile(dest); string(b) != "old" {
					t.Errorf("dest changed to %q", b)
				}
				if m, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".dockmesh-agent.*")); len(m) != 0 {
					t.Errorf("temp files left behind: %v", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			fi, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o755 {
				t.Errorf("dest mode %v, want regular 0755", fi.Mode())
			}
			if b, _ := os.ReadFile(dest); string(b) != "new" {
				t.Errorf("dest content %q, want %q", b, "new")
			}
			if b, _ := os.ReadFile(binary); string(b) != "new" {
				t.Errorf("binary changed to %q", b)
			}
		})
	}
}

func TestInstallAgentUnitRefreshesPathCopy(t *testing.T) {
	b, err := os.ReadFile("install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "ExecStartPre=-+$REFRESH_SCRIPT") {
		t.Error("unit does not refresh the /usr/local/bin copy at start")
	}
	if strings.Contains(s, "ln -sf \"$AGENT_BIN\"") {
		t.Error("installer still links the agent into the PATH")
	}
}

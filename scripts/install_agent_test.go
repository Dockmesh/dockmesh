package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		{"enrollment rejected", `{"msg":"enrolling"}` + "\n" + `{"level":"ERROR","msg":"enrollment failed","err":"enroll http 401: invalid or expired enrollment token"}`, "rejected"},
		{"enrollment rate limited", `{"level":"ERROR","msg":"enrollment failed","err":"enroll http 429: too many requests"}`, "failed"},
		{"enrollment tls error", `{"level":"ERROR","msg":"enrollment failed","err":"x509: certificate signed by unknown authority"}`, "failed"},
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
	withDataDB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withDataDB, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withDataDB, "data", "dockmesh.db"), nil, 0o600); err != nil {
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
		{"agent default", "/var/lib/dockmesh-agent", false},
		{"contains database", withDB, true},
		{"contains database in data", withDataDB, true},
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

// selinuxStubs returns a PATH that holds only the tools label_bin_t needs
// besides the SELinux ones, plus logging stubs for those named in stubs
// (name to exit code). Calls are appended to the returned log file.
func selinuxStubs(t *testing.T, stubs map[string]int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	for _, tool := range []string{"sed", "readlink", "dirname", "stat"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, code := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >>" + calls + "\nexit " + strconv.Itoa(code) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, calls
}

func TestInstallAgentStartFailureHint(t *testing.T) {
	fn := agentScriptFunc(t, "selinux_enabled") + agentScriptFunc(t, "start_failure_hint")
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	tests := []struct {
		name    string
		selinux int
		want    []string
		notWant []string
	}{
		{"selinux on", 0, []string{"SELinux is Enforcing", "needs the type bin_t", "ausearch -m avc", "restorecon -v /x/bin/dockmesh-agent", "systemctl status dockmesh-agent"}, nil},
		{"selinux off", 1, []string{"systemctl status dockmesh-agent", "journalctl -u dockmesh-agent"}, []string{"SELinux"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pathDir, _ := selinuxStubs(t, map[string]int{"selinuxenabled": tt.selinux})
			if err := os.WriteFile(filepath.Join(pathDir, "getenforce"), []byte("#!/bin/sh\necho Enforcing\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, "-c", fn+"\nstart_failure_hint /x/bin/dockmesh-agent\n")
			cmd.Env = []string{"PATH=" + pathDir}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(string(out), w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestInstallAgentWriteAgentFile(t *testing.T) {
	fn := agentScriptFunc(t, "write_agent_file")
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "enroll-token")
	if err := os.Symlink(victim, dest); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nprintf '%s\\n' \"$1\" | write_agent_file \"$2\" root root", "t", "tok_0123456789abcdef", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("enroll-token mode %v, want a regular 0600 file", fi.Mode())
	}
	if b, _ := os.ReadFile(dest); string(b) != "tok_0123456789abcdef\n" {
		t.Errorf("enroll-token = %q", b)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("symlink target written: %q", b)
	}
}

func TestInstallAgentLogField(t *testing.T) {
	fn := agentScriptFunc(t, "log_field")
	logs := `{"msg":"connected","agent_id":"a","agent_name":"old"}` + "\n" +
		`{"level":"ERROR","msg":"re-enrollment failed","err":"enrollment token rejected: enroll http 401"}` + "\n" +
		`{"msg":"connected","agent_id":"a","agent_name":"edge-1","server":"v"}`
	tests := []struct{ field, msg, want string }{
		{"agent_name", "connected", "edge-1"},
		{"err", "re-enrollment failed", "enrollment token rejected: enroll http 401"},
		{"err", "enrolled successfully", ""},
	}
	for _, tt := range tests {
		out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nlog_field \"$1\" \"$2\" \"$3\" || true", "t", logs, tt.field, tt.msg).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != tt.want {
			t.Errorf("log_field %s %s = %q, want %q", tt.field, tt.msg, got, tt.want)
		}
	}
}

func TestInstallAgentUnitUser(t *testing.T) {
	fn := agentScriptFunc(t, "unit_user")
	tests := []struct{ unit, want string }{
		{"[Service]\nUser=dockmesh-agent\nGroup=dockmesh-agent\n", "dockmesh-agent"},
		{"[Service]\nExecStart=/x\n", ""},
		{"[Service]\nUser=root\n", ""},
	}
	for _, tt := range tests {
		p := filepath.Join(t.TempDir(), "u.service")
		if err := os.WriteFile(p, []byte(tt.unit), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nunit_user \"$1\"", "t", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tt.want {
			t.Errorf("unit_user = %q, want %q", out, tt.want)
		}
	}
}

func TestInstallAgentPlainBox(t *testing.T) {
	fn := agentScriptFunc(t, "box")
	script := "BOX_TL=+ BOX_TR=+ BOX_BL=+ BOX_BR=+ BOX_H=- BOX_V='|'\n" + fn + "\nbox 'Agent installed' 'Host        edge-1'"
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.Trim(string(out), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "+ Agent installed -") || !strings.Contains(lines[2], "Host        edge-1") {
		t.Fatalf("box:\n%s", out)
	}
	for _, l := range lines {
		if len(l) != len(lines[0]) {
			t.Errorf("ragged box line %q", l)
		}
		if strings.ContainsRune(l, '\033') {
			t.Errorf("colour in plain box: %q", l)
		}
	}
}

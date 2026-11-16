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

func TestInstallAgentLabelBinT(t *testing.T) {
	fn := agentScriptFunc(t, "selinux_enabled") + agentScriptFunc(t, "label_bin_t")
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	all := map[string]int{"selinuxenabled": 0, "semanage": 0, "restorecon": 0, "chcon": 0}
	tests := []struct {
		name       string
		dir        string // bin dir below the temp dir
		binLink    bool   // the binary is a symlink to a file outside
		dirLink    bool   // the bin dir is a symlink to a directory outside
		file       bool   // label a single file instead of dir + binary
		stubs      map[string]int
		semanageM  bool // semanage -a fails, -m succeeds
		enforce    bool // SELinux detected through selinuxfs instead of selinuxenabled
		wantCalls  []string
		wantChcon  bool
		wantFailed bool
	}{
		{
			name:  "selinux disabled",
			dir:   "bin",
			stubs: map[string]int{"selinuxenabled": 1, "semanage": 0, "restorecon": 0, "chcon": 0},
		},
		{
			name:      "semanage rule, dir and binary only",
			dir:       "bin",
			stubs:     all,
			wantCalls: []string{"semanage fcontext -a -t bin_t @/bin(/.*)?", "restorecon -- @/bin @/bin/dockmesh-agent"},
		},
		{
			name:      "single file",
			dir:       "bin",
			file:      true,
			stubs:     all,
			wantCalls: []string{"semanage fcontext -a -t bin_t @/bin/dockmesh-agent", "restorecon -- @/bin/dockmesh-agent"},
		},
		{
			name:      "selinuxfs without selinuxenabled",
			dir:       "bin",
			stubs:     map[string]int{"semanage": 0, "restorecon": 0},
			enforce:   true,
			wantCalls: []string{"semanage fcontext -a -t bin_t @/bin(/.*)?", "restorecon -- @/bin @/bin/dockmesh-agent"},
		},
		{
			name:      "regex characters escaped",
			dir:       "data.dir+1/bin",
			stubs:     all,
			wantCalls: []string{`semanage fcontext -a -t bin_t @/data\.dir\+1/bin(/.*)?`, `restorecon -- @/data.dir+1/bin @/data.dir+1/bin/dockmesh-agent`},
		},
		{
			name:      "rule exists",
			dir:       "bin",
			stubs:     all,
			semanageM: true,
			wantCalls: []string{
				"semanage fcontext -a -t bin_t @/bin(/.*)?",
				"semanage fcontext -m -t bin_t @/bin(/.*)?",
				"restorecon -- @/bin @/bin/dockmesh-agent",
			},
		},
		{
			name:      "no semanage",
			dir:       "bin",
			stubs:     map[string]int{"selinuxenabled": 0, "restorecon": 0, "chcon": 0},
			wantCalls: []string{"chcon -t bin_t -- @/bin @/bin/dockmesh-agent"},
			wantChcon: true,
		},
		{
			name:       "chcon fails",
			dir:        "bin",
			stubs:      map[string]int{"selinuxenabled": 0, "chcon": 1},
			wantCalls:  []string{"chcon -t bin_t -- @/bin @/bin/dockmesh-agent"},
			wantChcon:  true,
			wantFailed: true,
		},
		{
			name:       "symlinked bin dir refused",
			dir:        "bin",
			dirLink:    true,
			stubs:      all,
			wantFailed: true,
		},
		{
			name:       "symlinked binary refused",
			dir:        "bin",
			binLink:    true,
			stubs:      all,
			wantFailed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pathDir, calls := selinuxStubs(t, tt.stubs)
			if tt.semanageM {
				script := "#!/bin/sh\necho \"semanage $*\" >>" + calls + "\n[ \"$2\" = -m ]\n"
				if err := os.WriteFile(filepath.Join(pathDir, "semanage"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(outside, "secret"), "x")
			target := filepath.Join(base, tt.dir)
			if tt.dirLink {
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(target, "dockmesh-agent")
			switch {
			case tt.binLink:
				if err := os.Symlink(filepath.Join(outside, "secret"), binary); err != nil {
					t.Fatal(err)
				}
			case !tt.dirLink:
				mustWrite(t, binary, "agent")
			}
			enforce := filepath.Join(base, "no-selinuxfs")
			if tt.enforce {
				enforce = filepath.Join(base, "enforce")
				mustWrite(t, enforce, "1")
			}
			args := []string{target, binary}
			if tt.file {
				args = []string{binary}
			}
			script := "warn() { echo \"$*\" >&2; }\n" + fn + "\nSELINUX_CHCON=0\nlabel_bin_t \"$@\"; rc=$?\necho \"chcon=$SELINUX_CHCON\"\nexit $rc\n"
			cmd := exec.Command(bash, append([]string{"-c", script, "t"}, args...)...)
			cmd.Env = []string{"PATH=" + pathDir, "SELINUX_ENFORCE_FILE=" + enforce}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantFailed {
				t.Fatalf("err %v, want failure %v: %s", err, tt.wantFailed, out)
			}
			wantChcon := "chcon=0"
			if tt.wantChcon {
				wantChcon = "chcon=1"
			}
			if !strings.Contains(string(out), wantChcon) {
				t.Errorf("output %q, want %s", out, wantChcon)
			}
			b, _ := os.ReadFile(calls)
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), base, "@")), "\n") {
				if line != "" && !strings.HasPrefix(line, "selinuxenabled") {
					got = append(got, line)
				}
			}
			if strings.Join(got, "\n") != strings.Join(tt.wantCalls, "\n") {
				t.Errorf("calls\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.wantCalls, "\n"))
			}
		})
	}
}

// TestInstallAgentRefusesSymlinkedBinDir checks that the installer stops
// before it creates, chmods or labels a bin dir the service account
// replaced with a symlink.
func TestInstallAgentRefusesSymlinkedBinDir(t *testing.T) {
	b, err := os.ReadFile("install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	refuse := strings.Index(s, `[[ -L "$BIN_DIR" ]] && die`)
	mkdir := strings.Index(s, `install -d -m 0750 -o "$RUN_USER" -g "$RUN_GROUP" "$BIN_DIR"`)
	if refuse < 0 || mkdir < 0 || refuse > mkdir {
		t.Error("the symlinked bin dir is not refused before the installer creates it")
	}
	if !strings.Contains(s, `BIN_DIR="$(readlink -f -- "$DATA_DIR")/bin"`) {
		t.Error("BIN_DIR resolves more than the root-controlled data dir")
	}
	if strings.Contains(agentScriptFunc(t, "label_bin_t"), " -R") {
		t.Error("label_bin_t relabels recursively")
	}
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
		{"selinux on", 0, []string{"SELinux is Enforcing", "needs the type bin_t", "ausearch -m avc", "chcon -t bin_t /x/bin /x/bin/dockmesh-agent", "systemctl status dockmesh-agent"}, nil},
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

func TestInstallAgentRefreshScriptRelabelsPathCopy(t *testing.T) {
	fn := agentScriptFunc(t, "install_path_copy")
	if !strings.Contains(fn, "restorecon -- \"$dest\"") {
		t.Error("install_path_copy does not reset the SELinux label of the copy")
	}
}

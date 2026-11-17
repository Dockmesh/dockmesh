//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// testInstaller runs system install inside a temp dir, as the test's own
// account for both root and the agent account.
type testInstaller struct {
	*agentInstaller
	root  string
	calls []string
	// opts lets a setup pick options that name its temp dir.
	opts installOptions
}

func newTestInstaller(t *testing.T) *testInstaller {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"bin", "etc", "units", "srv", "var"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self := filepath.Join(root, "dockmesh-agent-new")
	if err := os.WriteFile(self, []byte("agent binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	ti := &testInstaller{root: root}
	uid, gid := os.Getuid(), os.Getgid()
	ti.agentInstaller = &agentInstaller{
		self:          self,
		binary:        filepath.Join(root, "bin", "dockmesh-agent"),
		envFile:       filepath.Join(root, "etc", "dockmesh-agent", "agent.env"),
		unitDir:       filepath.Join(root, "units"),
		rootUID:       uid,
		rootGID:       gid,
		lookupAccount: func(string) (int, int, error) { return uid, gid, nil },
		dockerGID:     func() int { return 999 },
		socketGroups:  func(string, int) ([]string, string) { return []string{"docker"}, "" },
		rootOnly:      func(string) bool { return true },
		checkStacks: func(path, _, _ string, _ int, _ map[string]string) stacksDirResult {
			if strings.HasPrefix(path, "/etc") {
				return stacksDirResult{StacksDirCheck: agents.StacksDirCheck{Status: "fail", Message: "system dir /etc"}}
			}
			return stacksDirResult{StacksDirCheck: agents.StacksDirCheck{Status: "ok"}}
		},
		legacy: func(dataDir string, _ bool) error {
			for _, p := range []string{filepath.Join(root, "etc", "dockmesh-agent", "refresh-path-copy"), filepath.Join(dataDir, "bin", "dockmesh-agent")} {
				if _, err := os.Lstat(p); err == nil {
					return errors.New(p + " still exists")
				}
			}
			return nil
		},
		legacyScript: filepath.Join(root, "etc", "dockmesh-agent", "refresh-path-copy"),
		systemctl: func(args ...string) error {
			ti.calls = append(ti.calls, strings.Join(args, " "))
			return nil
		},
	}
	return ti
}

func (ti *testInstaller) path(p string) string { return filepath.Join(ti.root, p) }

func (ti *testInstaller) write(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ti.path(p)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ti.path(p), []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func (ti *testInstaller) read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(ti.path(p))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// snapshot is every file below the temp root with its content and mode.
func (ti *testInstaller) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(ti.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		v := fi.Mode().String()
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(data)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const legacyUnit = `[Service]
User=dockmesh-agent
Group=dockmesh-agent
EnvironmentFile=/etc/dockmesh-agent/agent.env
ExecStart=/var/lib/dockmesh-agent/bin/dockmesh-agent
`

const legacyRootUnit = `[Service]
EnvironmentFile=/etc/dockmesh-agent/agent.env
ExecStart=/var/lib/dockmesh-agent/bin/dockmesh-agent
`

func TestSystemInstall(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the host; it gets the data dir it should use.
		setup func(t *testing.T, ti *testInstaller, data string)
		opts  func(ti *testInstaller) installOptions
		// wantEnv are lines agent.env must hold and noEnv lines it must
		// not, wantUnit lines the unit must hold and noUnit lines it must not.
		wantEnv, noEnv, wantUnit, noUnit []string
	}{
		{
			name:  "fresh",
			setup: func(*testing.T, *testInstaller, string) {},
			opts: func(ti *testInstaller) installOptions {
				return installOptions{DataDir: ti.path("var/agent"), StacksDir: ti.path("srv/stacks"), SocketGID: "4242"}
			},
			wantEnv:  []string{"DOCKMESH_DATA_DIR=<root>/var/agent", "DOCKMESH_STACKS_DIR=<root>/srv/stacks", "DOCKMESH_SOCKET_GID=4242"},
			wantUnit: []string{"User=dockmesh-agent", "SupplementaryGroups=docker 4242", `ReadWritePaths="<root>/var/agent"`, `ReadWritePaths="-<root>/srv/stacks"`, "ExecStart=<root>/bin/dockmesh-agent"},
		},
		{
			name: "legacy",
			setup: func(t *testing.T, ti *testInstaller, data string) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+data+"\nDOCKMESH_ENROLL_URL=https://srv:8443/enroll\nDOCKMESH_TOKEN=spent\n", 0o640)
				ti.write(t, "units/dockmesh-agent.service", legacyUnit, 0o644)
			},
			opts:     func(*testInstaller) installOptions { return installOptions{} },
			wantEnv:  []string{"DOCKMESH_DATA_DIR=<root>/var/agent", "DOCKMESH_ENROLL_URL=https://srv:8443/enroll", "DOCKMESH_TOKEN=spent"},
			wantUnit: []string{"User=dockmesh-agent", `ReadWritePaths="<root>/var/agent"`},
			noUnit:   []string{"ExecStartPre", "DOCKMESH_STACKS_DIR"},
		},
		{
			name: "enrolled legacy drops the spent token",
			setup: func(t *testing.T, ti *testInstaller, data string) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+data+"\nDOCKMESH_TOKEN=spent\nDOCKMESH_CA_FINGERPRINT=ab\n", 0o640)
				ti.write(t, "units/dockmesh-agent.service", legacyUnit, 0o644)
				ti.write(t, "var/agent/agent.crt", "cert", 0o600)
			},
			opts:     func(*testInstaller) installOptions { return installOptions{} },
			wantEnv:  []string{"DOCKMESH_DATA_DIR=<root>/var/agent", "DOCKMESH_CA_FINGERPRINT=ab"},
			noEnv:    []string{"DOCKMESH_TOKEN"},
			wantUnit: []string{"User=dockmesh-agent"},
		},
		{
			name: "root",
			setup: func(t *testing.T, ti *testInstaller, data string) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+data+"\n", 0o640)
				ti.write(t, "units/dockmesh-agent.service", legacyRootUnit, 0o644)
			},
			opts:     func(*testInstaller) installOptions { return installOptions{} },
			wantEnv:  []string{"DOCKMESH_DATA_DIR=<root>/var/agent"},
			wantUnit: []string{`ReadWritePaths="<root>/var/agent"`},
			noUnit:   []string{"User=", "SupplementaryGroups="},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := newTestInstaller(t)
			data := ti.path("var/agent")
			tt.setup(t, ti, data)
			ti.write(t, "units/dockmesh-agent.service.d/local.conf", "[Service]\nEnvironment=FOO=1\n", 0o644)
			res, err := ti.run(tt.opts(ti))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.changed) == 0 || !res.unitsChanged {
				t.Fatalf("first run changed nothing: %+v", res)
			}
			expand := func(s string) string { return strings.ReplaceAll(s, "<root>", ti.root) }
			env := ti.read(t, "etc/dockmesh-agent/agent.env")
			for _, l := range tt.wantEnv {
				if !strings.Contains(env, expand(l)+"\n") {
					t.Errorf("agent.env lacks %q:\n%s", expand(l), env)
				}
			}
			for _, l := range tt.noEnv {
				if strings.Contains(env, l) {
					t.Errorf("agent.env has %q:\n%s", l, env)
				}
			}
			unit := ti.read(t, "units/dockmesh-agent.service")
			for _, l := range tt.wantUnit {
				if !strings.Contains(unit, expand(l)+"\n") {
					t.Errorf("unit lacks %q:\n%s", expand(l), unit)
				}
			}
			for _, l := range tt.noUnit {
				if strings.Contains(unit, l) {
					t.Errorf("unit has %q:\n%s", l, unit)
				}
			}
			if got := ti.read(t, "bin/dockmesh-agent"); got != "agent binary v2" {
				t.Errorf("binary = %q", got)
			}
			for _, f := range []string{"units/dockmesh-agent-update.path", "units/dockmesh-agent-update.service"} {
				ti.read(t, f)
			}

			before := ti.snapshot(t)
			res, err = ti.run(installOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.changed) != 0 || res.unitsChanged {
				t.Errorf("second run changed %v", res.changed)
			}
			after := ti.snapshot(t)
			for p, v := range before {
				if after[p] != v {
					t.Errorf("%s changed on the second run", p)
				}
			}
			if len(after) != len(before) {
				t.Errorf("second run left %d files, first %d", len(after), len(before))
			}
			if got := ti.read(t, "units/dockmesh-agent.service.d/local.conf"); got != "[Service]\nEnvironment=FOO=1\n" {
				t.Errorf("drop-in changed: %q", got)
			}
		})
	}
}

func TestSystemInstallRefuses(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ti *testInstaller)
		edit  func(ti *testInstaller)
		opts  installOptions
		want  string
	}{
		{
			name: "data dir of another account",
			setup: func(t *testing.T, ti *testInstaller) {
				if err := os.MkdirAll(ti.path("var/agent"), 0o700); err != nil {
					t.Fatal(err)
				}
				ti.opts = installOptions{DataDir: ti.path("var/agent")}
			},
			edit: func(ti *testInstaller) {
				ti.lookupAccount = func(string) (int, int, error) { return os.Getuid() + 1, os.Getgid(), nil }
			},
			want: "belongs to another account",
		},
		{
			name:  "stacks dir below a directory others may change",
			setup: func(*testing.T, *testInstaller) {},
			edit:  func(ti *testInstaller) { ti.rootOnly = func(string) bool { return false } },
			opts:  installOptions{DataDir: "/srv/agent", StacksDir: "/srv/stacks"},
			want:  "writable only by root",
		},
		{
			name: "legacy copy step still there",
			setup: func(t *testing.T, ti *testInstaller) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+ti.path("var/agent")+"\n", 0o640)
				ti.write(t, "etc/dockmesh-agent/refresh-path-copy", "#!/bin/bash\n", 0o755)
				ti.write(t, "units/dockmesh-agent.service", legacyUnit, 0o644)
			},
			want: "legacy agent layout",
		},
		{
			name: "legacy binary in the data dir",
			setup: func(t *testing.T, ti *testInstaller) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+ti.path("var/agent")+"\n", 0o640)
				ti.write(t, "var/agent/bin/dockmesh-agent", "old", 0o755)
			},
			want: "legacy agent layout",
		},
		{
			name: "other data dir",
			setup: func(t *testing.T, ti *testInstaller) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+ti.path("var/agent")+"\n", 0o640)
			},
			opts: installOptions{DataDir: "/srv/elsewhere"},
			want: "chosen at the first install only",
		},
		{
			name: "switch a root agent",
			setup: func(t *testing.T, ti *testInstaller) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+ti.path("var/agent")+"\n", 0o640)
				ti.write(t, "units/dockmesh-agent.service", legacyRootUnit, 0o644)
			},
			opts: installOptions{User: agentAccount},
			want: "does not switch",
		},
		{
			name:  "unknown account",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent", User: "nobody"},
			want:  "--user must be",
		},
		{
			name:  "stacks dir in a system tree",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent", StacksDir: "/etc/stacks"},
			want:  "/etc",
		},
		{
			name:  "stacks dir elsewhere in the data dir",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent", StacksDir: "/srv/agent/other"},
			want:  "lies in the data dir",
		},
		{
			name:  "stacks dir with a newline",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent", StacksDir: "/srv/stacks\nUser=root"},
			want:  "clean absolute path",
		},
		{
			name:  "data dir not clean",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent/../etc"},
			want:  "clean absolute path",
		},
		{
			name: "socket gid with a newline on a root agent",
			setup: func(t *testing.T, ti *testInstaller) {
				ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+ti.path("var/agent")+"\n", 0o640)
				ti.write(t, "units/dockmesh-agent.service", legacyRootUnit, 0o644)
			},
			opts: installOptions{SocketGID: "5\nDOCKMESH_X=1"},
			want: "--socket-gid",
		},
		{
			name:  "socket gid 0",
			setup: func(*testing.T, *testInstaller) {},
			opts:  installOptions{DataDir: "/srv/agent", SocketGID: "0"},
			want:  "--socket-gid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := newTestInstaller(t)
			tt.setup(t, ti)
			if tt.edit != nil {
				tt.edit(ti)
			}
			before := ti.snapshot(t)
			opts := tt.opts
			if ti.opts.DataDir != "" {
				opts = ti.opts
			}
			_, err := ti.run(opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			after := ti.snapshot(t)
			for p, v := range before {
				if after[p] != v {
					t.Errorf("%s changed", p)
				}
			}
			if len(after) != len(before) {
				t.Errorf("a refused run created files")
			}
		})
	}
}

func TestRenderAgentEnv(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		set   map[string]string
		want  string
	}{
		{"new file", nil, map[string]string{"DOCKMESH_DATA_DIR": "/d", "DOCKMESH_STACKS_DIR": ""}, "DOCKMESH_DATA_DIR=/d\n"},
		{"kept order and comments", []string{"# agent", "DOCKMESH_TOKEN=x", "DOCKMESH_DATA_DIR=/old"},
			map[string]string{"DOCKMESH_DATA_DIR": "/d"}, "# agent\nDOCKMESH_TOKEN=x\nDOCKMESH_DATA_DIR=/d\n"},
		{"duplicate dropped", []string{"DOCKMESH_STACKS_DIR=/a", "X=1", "export DOCKMESH_STACKS_DIR=/b"},
			map[string]string{"DOCKMESH_STACKS_DIR": "/c"}, "DOCKMESH_STACKS_DIR=/c\nX=1\n"},
		{"empty value removes", []string{"DOCKMESH_STACKS_DIR=/a", "X=1"},
			map[string]string{"DOCKMESH_STACKS_DIR": ""}, "X=1\n"},
	}
	for _, tt := range tests {
		if got := string(renderAgentEnv(tt.lines, tt.set)); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestActivate(t *testing.T) {
	ti := newTestInstaller(t)
	if err := ti.activate(true); err != nil {
		t.Fatal(err)
	}
	if err := ti.activate(false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"daemon-reload",
		"enable dockmesh-agent.service dockmesh-agent-update.path dockmesh-agent-update.service",
		"restart dockmesh-agent-update.path",
		"enable dockmesh-agent.service dockmesh-agent-update.path dockmesh-agent-update.service",
		"start dockmesh-agent-update.path",
	}
	if strings.Join(ti.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("systemctl calls:\n%s", strings.Join(ti.calls, "\n"))
	}
}

func TestSystemInstallConvertsLegacy(t *testing.T) {
	ti := newTestInstaller(t)
	data := ti.path("var/agent")
	ti.write(t, "etc/dockmesh-agent/agent.env", "DOCKMESH_DATA_DIR="+data+"\nDOCKMESH_TOKEN=spent\n", 0o640)
	ti.write(t, "etc/dockmesh-agent/refresh-path-copy", "#!/bin/bash\n", 0o755)
	ti.write(t, "units/dockmesh-agent.service", legacyUnit, 0o644)
	ti.write(t, "var/agent/bin/dockmesh-agent", "old binary", 0o755)
	ti.write(t, "var/agent/agent.crt", "cert", 0o600)
	if err := os.Chmod(data, 0o711); err != nil {
		t.Fatal(err)
	}

	if _, err := ti.run(installOptions{}); err == nil || !strings.Contains(err.Error(), "--convert-legacy") {
		t.Fatalf("run without --convert-legacy: %v", err)
	}
	if got := ti.read(t, "units/dockmesh-agent.service"); got != legacyUnit {
		t.Fatalf("refused run changed the unit:\n%s", got)
	}

	res, err := ti.run(installOptions{
		ConvertLegacy: true,
		EnrollURL:     "https://srv:8443/enroll",
		AgentURL:      "wss://srv:8443/connect",
		CAFingerprint: "AB:" + strings.Repeat("cd", 31),
		EnrollCAFile:  data + "/enroll-ca.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(data); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode %v, want 0700 (err %v)", fi.Mode(), err)
	}
	if len(res.removed) != 2 {
		t.Errorf("removed %v, want the copy script and the data dir binary", res.removed)
	}
	for _, p := range []string{"etc/dockmesh-agent/refresh-path-copy", "var/agent/bin"} {
		if _, err := os.Lstat(ti.path(p)); err == nil {
			t.Errorf("%s still there", p)
		}
	}
	unit := ti.read(t, "units/dockmesh-agent.service")
	if strings.Contains(unit, "ExecStartPre") || !strings.Contains(unit, "ExecStart="+ti.path("bin/dockmesh-agent")+"\n") {
		t.Errorf("unit:\n%s", unit)
	}
	env := ti.read(t, "etc/dockmesh-agent/agent.env")
	for _, l := range []string{"DOCKMESH_ENROLL_URL=https://srv:8443/enroll", "DOCKMESH_AGENT_URL=wss://srv:8443/connect",
		"DOCKMESH_CA_FINGERPRINT=ab" + strings.Repeat("cd", 31), "DOCKMESH_ENROLL_CA_FILE=" + data + "/enroll-ca.pem"} {
		if !strings.Contains(env, l+"\n") {
			t.Errorf("agent.env lacks %q:\n%s", l, env)
		}
	}
	if strings.Contains(env, "DOCKMESH_TOKEN") {
		t.Errorf("agent.env keeps the spent token:\n%s", env)
	}

	res, err = ti.run(installOptions{ConvertLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.changed)+len(res.removed) != 0 {
		t.Errorf("second run changed %v, removed %v", res.changed, res.removed)
	}
}

func TestConnectionSettings(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	tests := []struct {
		name string
		o    installOptions
		ok   bool
	}{
		{"none", installOptions{}, true},
		{"all", installOptions{EnrollURL: "https://10.0.0.1:8443/enroll", AgentURL: "wss://h.example:8443/connect", CAFingerprint: fp, EnrollCAFile: "/var/lib/dockmesh-agent/enroll-ca.pem"}, true},
		{"http enroll", installOptions{EnrollURL: "http://h/enroll"}, false},
		{"ws agent url", installOptions{AgentURL: "ws://h/connect"}, false},
		{"newline in url", installOptions{EnrollURL: "https://h/enroll\nDOCKMESH_DATA_DIR=/etc"}, false},
		{"dollar in url", installOptions{AgentURL: "wss://h/$HOME"}, false},
		{"no host", installOptions{EnrollURL: "https:///enroll"}, false},
		{"short fingerprint", installOptions{CAFingerprint: "abcd"}, false},
		{"relative ca file", installOptions{EnrollCAFile: "enroll-ca.pem"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := connectionSettings(tt.o)
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestSystemInstallKeepsDataDirMode(t *testing.T) {
	ti := newTestInstaller(t)
	data := ti.path("var/agent")
	ti.write(t, "var/agent/agent.crt", "cert", 0o600)
	if err := os.Chmod(data, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := ti.run(installOptions{DataDir: data}); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(data); err != nil || fi.Mode().Perm() != 0o750 {
			t.Fatalf("run %d: data dir mode %v, want the admin's 0750", i+1, fi.Mode())
		}
	}
}

func TestServerDataDir(t *testing.T) {
	tests := []struct {
		name, dir string
		files     []string
		want      bool
	}{
		{"database", "/srv/x", []string{"/srv/x/dockmesh.db"}, true},
		{"database in data", "/srv/x", []string{"/srv/x/data/dockmesh.db"}, true},
		{"env file", "/srv/x", []string{"/srv/x/dockmesh.env"}, true},
		{"agent only", legacyDataDir, nil, false},
		{"server env in /etc", legacyDataDir, []string{"/r/etc/dockmesh/dockmesh.env"}, true},
		{"server unit", legacyDataDir, []string{"/r/etc/systemd/system/dockmesh.service"}, true},
		{"server unit, other dir", "/srv/x", []string{"/r/etc/systemd/system/dockmesh.service"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := func(p string) bool { return slices.Contains(tt.files, p) }
			if got := serverDataDirAt(tt.dir, "/r", exists); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

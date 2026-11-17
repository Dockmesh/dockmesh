package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeHost struct {
	t       *testing.T
	root    string
	calls   []string
	deleted []string
	active  bool
	// ack makes the fake agent confirm the uninstalled frame.
	ack     bool
	running []runningContainer
	uid     int
}

func newFakeHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, root: t.TempDir(), uid: os.Getuid(),
		running: []runningContainer{{project: "web"}, {project: "db"}, {project: "other"}}}
}

func (h *fakeHost) file(p, data string) {
	h.t.Helper()
	full := filepath.Join(h.root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) dir(p string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, p), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(h.root, p))
	return err == nil
}

// managed lays out an agent of the managed layout with two stacks.
func (h *fakeHost) managed(data string) {
	for _, f := range agentUnits {
		h.file(f, "[Unit]\n")
	}
	h.file(pathBinary, "bin")
	h.file(agentEnvFile, "DOCKMESH_DATA_DIR="+data+"\n")
	for _, f := range []string{"agent.crt", "agent.key", "ca.crt", "agent.url", "enroll-ca.pem"} {
		h.file(filepath.Join(data, f), "x")
	}
	h.file(filepath.Join(data, agentNameFile), "edge-1\n")
	h.file(filepath.Join(data, "staging", "web-123", "compose.yaml"), "x")
	h.file(filepath.Join(data, "update", "running"), "v1")
	h.file(filepath.Join(data, "stacks", "web", "compose.yaml"), "x")
	h.file(filepath.Join(data, "stacks", "web", ".dockmesh.meta.json"), "{}")
	h.dir(filepath.Join(data, "stacks", "web", "data"))
	h.file(filepath.Join(data, "stacks", "db", "compose.yaml"), "x")
	h.file(filepath.Join(managedStateDir, "stacks-dir.lock"), "")
}

func (h *fakeHost) uninstaller(env map[string]string) *uninstaller {
	return &uninstaller{
		root:     h.root,
		env:      env,
		hostname: "host-os-name",
		systemctl: func(args ...string) error {
			h.calls = append(h.calls, strings.Join(args, " "))
			return nil
		},
		unitActive: func() bool { return h.active },
		notify: func() error {
			if h.ack {
				h.file(filepath.Join(defaultDataDir, uninstalledFile), "removed\n")
			}
			return nil
		},
		notifyWait: time.Second,
		containers: func() []runningContainer { return h.running },
		lookupUser: func(name string) (int, string, bool) { return h.uid, "Dockmesh Agent", name == agentAccount },
		deleteUser: func(name string) error {
			h.deleted = append(h.deleted, name)
			return nil
		},
		lockState: func() (func(), error) { return func() {}, nil },
		// Files in the temp root belong to the test's own account.
		systemUIDTo: math.MaxInt32,
	}
}

func runUninstall(t *testing.T, u *uninstaller, o uninstallOptions) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := u.run(o, strings.NewReader(""), &out)
	return out.String(), err
}

func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Errorf("output lacks %q:\n%s", l, out)
		}
	}
}

func TestUninstallDefaultKeepsStacksAndUser(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out,
		"Dockmesh agent uninstall · edge-1\n",
		"  remove  dockmesh-agent.service, dockmesh-agent-update.path/.service\n",
		"  remove  /usr/local/bin/dockmesh-agent\n",
		"  remove  /etc/dockmesh-agent\n",
		"  remove  certificate and key, staging/, update/ in /var/lib/dockmesh-agent\n",
		"  keep    /var/lib/dockmesh-agent/stacks (2 stacks): kept by default, use --purge to remove\n",
		"  keep    user dockmesh-agent: kept by default, use --purge to remove\n",
		"! 2 containers of Dockmesh stacks are running. They keep running.\n",
		"  Manage them with docker compose in /var/lib/dockmesh-agent/stacks/<stack>.\n",
		"ok  The agent is removed. Delete edge-1 in Dockmesh: Hosts › edge-1 › Maintenance › Revoke.\n",
	)
	for _, gone := range []string{agentUnitFile, managedPathUnit, managedServiceUnit, pathBinary, "/etc/dockmesh-agent",
		defaultDataDir + "/agent.key", defaultDataDir + "/staging", defaultDataDir + "/update", managedStateDir} {
		if h.exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	for _, kept := range []string{defaultDataDir + "/stacks/web/compose.yaml", defaultDataDir + "/stacks/web/data", defaultDataDir + "/stacks/db/compose.yaml"} {
		if !h.exists(kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	if len(h.deleted) != 0 {
		t.Errorf("user deleted without --purge: %v", h.deleted)
	}
	if !slices.Contains(h.calls, "disable --now dockmesh-agent.service dockmesh-agent-update.path dockmesh-agent-update.service") {
		t.Errorf("units not disabled: %v", h.calls)
	}
}

func TestUninstallPurge(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out,
		"  remove  /var/lib/dockmesh-agent (data and stacks)\n",
		"  remove  user dockmesh-agent\n",
	)
	if strings.Contains(out, "  keep  ") || strings.Contains(out, "Kept:") {
		t.Errorf("clean purge kept something:\n%s", out)
	}
	if h.exists(defaultDataDir) || h.exists(managedStateDir) {
		t.Error("the data dir or root's state dir is still there")
	}
	if !slices.Equal(h.deleted, []string{agentAccount}) {
		t.Errorf("deleted users %v", h.deleted)
	}
}

func TestUninstallPurgeKeepsForeignFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(h *fakeHost, outside string)
		example string
	}{
		{"container data", func(h *fakeHost, _ string) {
			h.file(defaultDataDir+"/stacks/web/data/db.sqlite", "rows")
		}, "web/data/db.sqlite"},
		{"symlink", func(h *fakeHost, outside string) {
			if err := os.Symlink(outside, filepath.Join(h.root, defaultDataDir, "stacks", "web", "link")); err != nil {
				t.Fatal(err)
			}
		}, "web/link"},
		{"file next to the stacks", func(h *fakeHost, _ string) {
			h.file(defaultDataDir+"/stacks/notes.txt", "mine")
		}, "notes.txt"},
		{"extra file in a stack", func(h *fakeHost, _ string) {
			h.file(defaultDataDir+"/stacks/db/init.sql", "mine")
		}, "db/init.sql"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.managed(defaultDataDir)
			outside := filepath.Join(h.root, "outside")
			h.file("outside/precious", "keep me")
			tc.setup(h, outside)
			out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true, purge: true})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wantLines(t, out,
				"  keep    /var/lib/dockmesh-agent/stacks: contains files not written by Dockmesh (e.g. "+tc.example+")\n",
				"  keep    /var/lib/dockmesh-agent: contains files not written by Dockmesh (e.g. stacks)\n",
				"  keep    user dockmesh-agent: owns the kept /var/lib/dockmesh-agent/stacks\n",
				"Kept: /var/lib/dockmesh-agent/stacks, /var/lib/dockmesh-agent. Remove it yourself if you no longer need it.\n",
			)
			for _, kept := range []string{defaultDataDir + "/stacks/web/compose.yaml", defaultDataDir + "/stacks/db/compose.yaml", "outside/precious"} {
				if !h.exists(kept) {
					t.Errorf("%s was removed", kept)
				}
			}
			if h.exists(defaultDataDir + "/agent.key") {
				t.Error("the agent's key is still there")
			}
			if len(h.deleted) != 0 {
				t.Errorf("user deleted although it owns kept files: %v", h.deleted)
			}
		})
	}
}

func TestUninstallLegacyLayout(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	h.file(legacyRefreshScript, "#!/bin/sh\n")
	h.file(defaultDataDir+"/bin/dockmesh-agent", "bin")
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "  remove  /var/lib/dockmesh-agent/bin/dockmesh-agent and /usr/local/bin/dockmesh-agent\n")
	for _, gone := range []string{legacyRefreshScript, defaultDataDir, pathBinary} {
		if h.exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
}

func TestUninstallStacksDirOutsideData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded bool
		wantDir  bool
		wantLine string
	}{
		{"created by root", true, false, "  remove  /srv/stacks (1 stack)\n"},
		{"prepared by the admin", false, true, "  keep    /srv/stacks: the directory was not created by Dockmesh; it is empty now\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.managed(defaultDataDir)
			h.file("/srv/stacks/app/compose.yaml", "x")
			if tc.recorded {
				h.file(filepath.Join(managedStateDir, createdDirsFile), "/srv/stacks\n")
			}
			env := map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir, "DOCKMESH_STACKS_DIR": "/srv/stacks"}
			out, err := runUninstall(t, h.uninstaller(env), uninstallOptions{yes: true, purge: true})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wantLines(t, out, tc.wantLine)
			if h.exists("/srv/stacks/app") {
				t.Error("the stack was not removed")
			}
			if h.exists("/srv/stacks") != tc.wantDir {
				t.Errorf("/srv/stacks exists = %v, want %v", !tc.wantDir, tc.wantDir)
			}
			if !h.exists("/srv") {
				t.Error("the parent of the stacks dir was removed")
			}
		})
	}
}

func TestUninstallSharedServerDataDir(t *testing.T) {
	h := newFakeHost(t)
	h.managed(legacyDataDir)
	h.file(legacyDataDir+"/data/dockmesh.db", "db")
	h.file("/etc/dockmesh/dockmesh.env", "x")
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": legacyDataDir}), uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out,
		"  keep    /var/lib/dockmesh/stacks (2 stacks): shared with the Dockmesh server on this host\n",
		"  keep    /var/lib/dockmesh: the Dockmesh server keeps its data here\n",
	)
	for _, kept := range []string{legacyDataDir + "/data/dockmesh.db", legacyDataDir + "/stacks/web/compose.yaml", "/etc/dockmesh/dockmesh.env"} {
		if !h.exists(kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	if h.exists(legacyDataDir + "/agent.key") {
		t.Error("the agent's key is still there")
	}
}

func TestUninstallChangesNothingUnasked(t *testing.T) {
	for _, tc := range []struct {
		name    string
		o       uninstallOptions
		wantErr bool
		header  string
	}{
		{"dry run", uninstallOptions{dryRun: true, purge: true}, false, "Dockmesh agent uninstall · edge-1 · dry run\n"},
		{"no terminal", uninstallOptions{purge: true}, true, "Dockmesh agent uninstall · edge-1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.managed(defaultDataDir)
			out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), tc.o)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v\n%s", err, out)
			}
			wantLines(t, out, tc.header)
			for _, kept := range []string{agentUnitFile, pathBinary, agentEnvFile, defaultDataDir + "/agent.key", defaultDataDir + "/stacks/web/compose.yaml", managedStateDir} {
				if !h.exists(kept) {
					t.Errorf("%s was removed", kept)
				}
			}
			if len(h.calls) != 0 {
				t.Errorf("systemctl called: %v", h.calls)
			}
		})
	}
}

func TestUninstallPrompts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		purge  bool
		answer string
		want   bool
	}{
		{"yes", false, "y\n", true},
		{"default no", false, "\n", false},
		{"purge with the host name", true, "edge-1\n", true},
		{"purge with y", true, "y\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.managed(defaultDataDir)
			u := h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir})
			u.interactive = true
			var out bytes.Buffer
			err := u.run(uninstallOptions{purge: tc.purge}, strings.NewReader(tc.answer), &out)
			if (err == nil) != tc.want {
				t.Fatalf("err = %v\n%s", err, out.String())
			}
			if h.exists(pathBinary) == tc.want {
				t.Errorf("binary exists = %v after answer %q", !tc.want, tc.answer)
			}
		})
	}
}

func TestUninstallTellsTheServer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		ack    bool
		want   string
	}{
		{"confirmed", true, true, `ok  The agent is removed. Dockmesh shows edge-1 as "Removed from host".`},
		{"no answer", true, false, "ok  The agent is removed. Delete edge-1 in Dockmesh: Hosts › edge-1 › Maintenance › Revoke."},
		{"agent stopped", false, true, "ok  The agent is removed. Delete edge-1 in Dockmesh: Hosts › edge-1 › Maintenance › Revoke."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.managed(defaultDataDir)
			h.active, h.ack = tc.active, tc.ack
			u := h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir})
			u.notifyWait = 300 * time.Millisecond
			out, err := runUninstall(t, u, uninstallOptions{yes: true})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wantLines(t, out, tc.want)
			if h.exists(defaultDataDir + "/" + uninstalledFile) {
				t.Error("the confirmation file was left behind")
			}
		})
	}
}

func TestUnitList(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"dockmesh-agent.service", "dockmesh-agent-update.path", "dockmesh-agent-update.service"}, "dockmesh-agent.service, dockmesh-agent-update.path/.service"},
		{[]string{"dockmesh-agent.service"}, "dockmesh-agent.service"},
		{[]string{"dockmesh-agent.service", "dockmesh-agent-update.service"}, "dockmesh-agent.service, dockmesh-agent-update.service"},
	} {
		if got := unitList(tc.in); got != tc.want {
			t.Errorf("unitList(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUninstallSharedKeepsServerUpdateFiles(t *testing.T) {
	for _, purge := range []bool{false, true} {
		h := newFakeHost(t)
		h.managed(legacyDataDir)
		h.file(legacyDataDir+"/data/dockmesh.db", "db")
		h.file(legacyDataDir+"/update/server-result.json", "{}")
		h.file(legacyDataDir+"/staging/server-stack/compose.yaml", "x")
		out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": legacyDataDir}), uninstallOptions{yes: true, purge: purge})
		if err != nil {
			t.Fatalf("purge=%v: %v\n%s", purge, err, out)
		}
		for _, kept := range []string{legacyDataDir + "/update/server-result.json", legacyDataDir + "/staging/server-stack/compose.yaml"} {
			if !h.exists(kept) {
				t.Errorf("purge=%v: the server's %s was removed", purge, kept)
			}
		}
	}
}

func TestUninstallKeepsMountsOfRunningContainers(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	h.running = append(h.running, runningContainer{project: "web", mounts: []string{defaultDataDir + "/stacks/web/data"}})
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "  keep    /var/lib/dockmesh-agent/stacks: contains files not written by Dockmesh (e.g. web/data (mounted by a running container))\n")
	if !h.exists(defaultDataDir + "/stacks/web/data") {
		t.Fatal("the bind mount of a running container was removed")
	}
}

func TestUninstallReplansAfterTheAgentStopped(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	h.file("victim/web/compose.yaml", "someone else's")
	h.active = true
	u := h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir})
	u.notifyWait = 50 * time.Millisecond
	// The agent's account swaps its stacks dir for a link after the plan.
	u.notify = func() error {
		stacks := filepath.Join(h.root, defaultDataDir, "stacks")
		if err := os.Rename(stacks, stacks+".old"); err != nil {
			t.Fatal(err)
		}
		return os.Symlink(filepath.Join(h.root, "victim"), stacks)
	}
	out, err := runUninstall(t, u, uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !h.exists("victim/web/compose.yaml") {
		t.Fatal("root removed a file through the swapped link")
	}
}

func TestUninstallPrintsNoTerminalControls(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	h.file(defaultDataDir+"/stacks/web/\x1b[2Jx", "mine")
	out, err := runUninstall(t, h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir}), uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("output carries an escape character: %q", out)
	}
}

func TestUninstallKeepsAccountsTheInstallerDidNotMake(t *testing.T) {
	h := newFakeHost(t)
	h.managed(defaultDataDir)
	u := h.uninstaller(map[string]string{"DOCKMESH_DATA_DIR": defaultDataDir})
	u.lookupUser = func(string) (int, string, bool) { return h.uid, "someone's service", true }
	out, err := runUninstall(t, u, uninstallOptions{yes: true, purge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "  keep    user dockmesh-agent: not the account the installer creates\n")
	if len(h.deleted) != 0 {
		t.Fatalf("deleted %v", h.deleted)
	}
}

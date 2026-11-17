//go:build unix

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

// testStacksRules checks paths below a temp root: directories above it
// count as root's, the ones inside must belong to the test's account and
// not be writable by others.
func testStacksRules(t *testing.T) (string, stacksDirRules) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"data/stacks", "docker", "srv/vol", "srv/apps/stacks", "full/x", "empty", "loose/prep", "nfs", "usb", "bind", "fuse", "cur/s",
		"opt/a", "opt/certbot/lib/python3.11/site-packages", "home", "mnt/disk1", "mnt/plain", "mnt/ram", "mnt/pool", "mnt/snap", "media"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "loose"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "srv"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	mounts := "22 1 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"40 22 0:50 / " + filepath.Join(root, "nfs") + " rw - nfs4 nas:/export rw\n" +
		"41 22 8:17 / " + filepath.Join(root, "usb") + " rw - vfat /dev/sdb1 rw\n" +
		"42 22 8:1 /etc/ssh " + filepath.Join(root, "bind") + " rw - ext4 /dev/sda1 rw\n" +
		"43 22 0:60 / " + filepath.Join(root, "fuse") + " rw - fuse.mergerfs pool rw\n" +
		"44 22 0:61 /@data " + filepath.Join(root, "srv") + " rw - btrfs /dev/sdc rw,subvol=/@data\n" +
		"45 22 8:33 / " + filepath.Join(root, "mnt", "disk1") + " rw - ext4 /dev/sdd1 rw\n" +
		"46 44 8:49 / " + filepath.Join(root, "srv", "vol") + " rw - xfs /dev/sde1 rw\n" +
		"47 22 0:70 / " + filepath.Join(root, "mnt", "ram") + " rw - tmpfs tmpfs rw\n" +
		"48 22 0:71 /@pool " + filepath.Join(root, "mnt", "pool") + " rw - btrfs /dev/sdf rw,subvol=/@pool\n" +
		"49 22 0:71 /@pool/sub " + filepath.Join(root, "mnt", "snap") + " rw - btrfs /dev/sdf rw,subvol=/@pool\n"
	me := uint32(os.Getuid())
	return root, stacksDirRules{
		dataDir:        filepath.Join(root, "data"),
		current:        filepath.Join(root, "data", "stacks"),
		serverDataDirs: []string{defaultServerDataDir, filepath.Join(root, "server")},
		dockerRoots:    []string{filepath.Join(root, "docker")},
		stackDirs:      []string{filepath.Join(root, "data", "stacks"), filepath.Join(root, "cur")},
		deny:           []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/efi", "/proc", "/sys", "/dev", "/run", "/root", "/var", "/snap", "/nix"},
		parents:        []string{filepath.Join(root, "srv"), filepath.Join(root, "opt"), filepath.Join(root, "mnt"), filepath.Join(root, "media")},
		uid:            os.Getuid(),
		account:        "dockmesh-agent",
		mountinfo:      func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(mounts)), nil },
		parentOK: func(p string, uid, mode uint32) bool {
			if !strings.HasPrefix(p, root+"/") {
				return true
			}
			return uid == me && mode&0o022 == 0
		},
	}
}

func TestCheckStacksDir(t *testing.T) {
	root, rules := testStacksRules(t)
	tests := []struct {
		name   string
		path   string
		edit   func(*stacksDirRules)
		issue  string
		ok     bool
		create bool
	}{
		{name: "new dir in an allowed parent", path: root + "/srv/stacks", ok: true, create: true},
		{name: "new dir in opt", path: root + "/opt/stacks", ok: true, create: true},
		{name: "new dir in media", path: root + "/media/stacks", ok: true, create: true},
		{name: "two new levels", path: root + "/opt/x/stacks", issue: sdNewNotHere},
		{name: "new dir below an existing plain dir", path: root + "/opt/a/stacks", issue: sdNewNotHere},
		{name: "new dir in a venv", path: root + "/opt/certbot/lib/python3.11/site-packages/sitecustomize", issue: sdNewNotHere},
		{name: "new dir at the top", path: root + "/stacks", issue: sdNewNotHere},
		{name: "new dir in /", path: "/dockmesh-x17-stacks", issue: sdNewNotHere},
		{name: "new dir in home", path: root + "/home/x", issue: sdNewNotHere},
		{name: "new dir on a disk mounted below mnt", path: root + "/mnt/disk1/stacks", ok: true, create: true},
		{name: "new dir on a disk mounted deeper", path: root + "/srv/vol/stacks", ok: true, create: true},
		{name: "new dir on a btrfs subvolume root", path: root + "/mnt/pool/stacks", ok: true, create: true},
		{name: "btrfs subvolume bound from a subdirectory", path: root + "/mnt/snap/stacks", issue: sdBindMount},
		{name: "new dir in a plain dir below mnt", path: root + "/mnt/plain/stacks", issue: sdNewNotHere},
		{name: "new dir on tmpfs below mnt", path: root + "/mnt/ram/stacks", issue: sdNewNotHere},
		{name: "new dir in a mount point outside the parents", path: root + "/nfs/x", issue: sdNetworkShare},
		{name: "prepared dir two levels down", path: root + "/srv/apps/stacks", ok: true},
		{name: "prepared dir anywhere", path: root + "/empty", ok: true},
		{name: "prepared dir of another account", path: root + "/srv/apps/stacks", edit: func(r *stacksDirRules) { r.uid = os.Getuid() + 1 }, issue: sdForeignOwner},
		{name: "prepared dir through a symlink", path: root + "/link/apps/stacks", issue: sdSymlink},
		{name: "prepared dir on a bind mount", path: root + "/bind", issue: sdBindMount},
		{name: "default dir", path: root + "/data/stacks2", issue: sdAgentData},
		{name: "unchanged", path: root + "/data/stacks", ok: true, issue: sdUnchanged},
		{name: "back to the default", path: root + "/data/stacks", edit: func(r *stacksDirRules) { r.current = root + "/srv" }, ok: true},
		{name: "relative", path: "srv/stacks", issue: sdNotAbsolute},
		{name: "dot dot", path: root + "/srv/../etc", issue: sdBadChars},
		{name: "trailing slash", path: root + "/srv/", issue: sdBadChars},
		{name: "space", path: root + "/srv/my stacks", issue: sdBadChars},
		{name: "newline", path: root + "/srv/x\nUser=root", issue: sdBadChars},
		{name: "root dir", path: "/", issue: sdSystemDir},
		{name: "etc", path: "/etc/x", issue: sdSystemDir},
		{name: "etc other case", path: "/ETC/x", issue: sdSystemDir},
		{name: "lib64", path: "/lib64/x", issue: sdSystemDir},
		{name: "libexec", path: "/libexec/x", issue: sdSystemDir},
		{name: "usr local", path: "/usr/local/stacks", issue: sdSystemDir},
		{name: "var lib docker", path: "/var/lib/docker/stacks", issue: sdSystemDir},
		{name: "run", path: "/run/stacks", issue: sdSystemDir},
		{name: "root home", path: "/root/stacks", issue: sdSystemDir},
		{name: "docker root", path: root + "/docker/stacks", issue: sdInsideDocker},
		{name: "docker root unknown", path: root + "/srv/x", edit: func(r *stacksDirRules) { r.dockerRoots = nil }, issue: sdDockerRoot},
		{name: "second docker root", path: root + "/srv/d/x", edit: func(r *stacksDirRules) { r.dockerRoots = append(r.dockerRoots, root+"/srv/d") }, issue: sdInsideDocker},
		{name: "var lib", path: "/var/lib/polkit-1/localauthority/60-dm.d", issue: sdSystemDir},
		{name: "var lib extensions", path: "/var/lib/extensions/dm", issue: sdSystemDir},
		{name: "tmp", path: "/tmp/stacks", edit: func(r *stacksDirRules) { r.deny = denyRoots }, issue: sdSystemDir},
		{name: "default dir in var", path: "/var/lib/dockmesh-agent/stacks", edit: func(r *stacksDirRules) { r.dataDir = "/var/lib/dockmesh-agent"; r.current = "/srv/s" }, ok: true, create: true},
		{name: "inside a stacks dir", path: root + "/cur/s/x", issue: sdInStacks},
		{name: "bind mount of a subdirectory", path: root + "/bind/x", issue: sdBindMount},
		{name: "fuse", path: root + "/fuse/stacks", issue: sdFUSE},
		{name: "inside the data dir", path: root + "/data/other", issue: sdAgentData},
		{name: "the data dir", path: root + "/data", issue: sdAgentData},
		{name: "server data dir", path: "/var/lib/dockmesh/stacks", issue: sdSystemDir},
		{name: "configured server data dir", path: root + "/server/stacks", issue: sdServerData},
		{name: "symlink in the path", path: root + "/link/stacks", issue: sdSymlink},
		{name: "symlink as the target", path: root + "/link", issue: sdSymlink},
		{name: "file in the path", path: root + "/file/stacks", issue: sdNotDir},
		{name: "parent others may write", path: root + "/loose/stacks", issue: sdUnsafeParent},
		{name: "prepared dir below a dir others may write", path: root + "/loose/prep", issue: sdUnsafeParent},
		{name: "not empty", path: root + "/full", issue: sdNotEmpty},
		{name: "other owner", path: root + "/empty", edit: func(r *stacksDirRules) { r.uid = os.Getuid() + 1 }, issue: sdForeignOwner},
		{name: "not empty, two levels", path: root + "/srv/apps", issue: sdNotEmpty},
		{name: "network share", path: root + "/nfs/stacks", issue: sdNetworkShare},
		{name: "no unix permissions", path: root + "/usb/stacks", issue: sdNoUnixPerms},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rules
			if tt.edit != nil {
				tt.edit(&r)
			}
			got := checkStacksDir(tt.path, r)
			lowSpaceOK := tt.ok && got.Issue == sdLowSpace
			if got.ok() != tt.ok || tt.issue != "" && got.Issue != tt.issue && !lowSpaceOK {
				t.Fatalf("checkStacksDir(%q) = %+v, want ok=%v issue=%q", tt.path, got.StacksDirCheck, tt.ok, tt.issue)
			}
			if tt.ok && tt.issue == "" && got.WillCreate != tt.create {
				t.Errorf("will_create = %v, want %v", got.WillCreate, tt.create)
			}
		})
	}
}

// FuzzCheckStacksDir: whatever the input, an accepted path is clean,
// absolute, plain, outside every refused tree and not the data dir.
func FuzzCheckStacksDir(f *testing.F) {
	for _, s := range []string{"/srv/x", "/etc/x", "/srv/../etc", "//srv", "/srv/./x", "/LIB/x", "/var/lib/docker", "/srv\x00/x", "/étc"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		rules := stacksDirRules{
			dataDir: "/var/lib/dockmesh-agent", current: "/var/lib/dockmesh-agent/stacks",
			serverDataDirs: []string{defaultServerDataDir}, dockerRoots: []string{"/var/lib/docker"}, uid: os.Getuid(),
			mountinfo: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil },
		}
		got := checkStacksDir(p, rules)
		if !got.ok() || got.Issue == sdUnchanged {
			return
		}
		if filepath.Clean(p) != p || !filepath.IsAbs(p) || strings.ContainsAny(p, " \n\t\"'\\$%") || deniedByName(p, denyRoots) ||
			layout.Within(rules.dataDir, p) && p != "/var/lib/dockmesh-agent/stacks" || layout.Within(defaultServerDataDir, p) {
			t.Fatalf("accepted %q: %+v", p, got.StacksDirCheck)
		}
		if got.WillCreate && p != "/var/lib/dockmesh-agent/stacks" && !slices.Contains(stacksParents, filepath.Dir(p)) {
			t.Fatalf("would create %q outside the allowed parents", p)
		}
	})
}

func TestKeptStackDirs(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	mk := func(n string, stacks bool) string {
		p := filepath.Join(root, n)
		if stacks {
			mkStack(t, p, "s")
		} else if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
		return p
	}
	def := filepath.Join(data, "stacks")
	mkStack(t, def, "s")
	a, b, empty := mk("a", true), mk("b", true), mk("e", false)
	tests := []struct {
		name    string
		target  string
		dirs    stackDirs
		want    []string
		wantErr bool
	}{
		{"default with stacks kept", root + "/new", stackDirs{current: def}, []string{def}, false},
		{"empty current dropped", root + "/new", stackDirs{current: empty}, nil, false},
		{"empty earlier dropped", root + "/new", stackDirs{current: a, earlier: []string{empty, def}}, []string{a, def}, false},
		{"two earlier plus target", root + "/new", stackDirs{current: a, earlier: []string{b}}, []string{a, b}, false},
		{"default does not count", root + "/new", stackDirs{current: a, earlier: []string{b, def}}, []string{a, b, def}, false},
		{"three earlier refused", root + "/new", stackDirs{current: a, earlier: []string{b, mk("c", true)}}, nil, true},
		{"back to the default", def, stackDirs{current: a, earlier: []string{def}}, []string{a}, false},
	}
	for _, tt := range tests {
		got, err := keptStackDirs(data, tt.target, tt.dirs)
		if (err != nil) != tt.wantErr || (!tt.wantErr && strings.Join(got, ":") != strings.Join(tt.want, ":")) {
			t.Errorf("%s: %v, %v; want %v, err %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCreateStacksDir(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "stacks")
	if err := createStacksDir(p, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(p); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750 {
		t.Errorf("%s: %v %v, want dir 0750", p, fi, err)
	}
	if err := createStacksDir(p, os.Getuid(), os.Getgid()); err == nil {
		t.Error("an existing directory was created again")
	}
	if err := createStacksDir(filepath.Join(root, "a", "b"), os.Getuid(), os.Getgid()); err == nil {
		t.Error("a missing parent was created")
	}
	if err := os.Symlink(root, filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	if err := createStacksDir(filepath.Join(root, "l", "x"), os.Getuid(), os.Getgid()); err == nil {
		t.Error("a symlink in the path was followed")
	}
}

type memUnits struct{ data []byte }

func (m *memUnits) ReadUnit() ([]byte, error)     { return m.data, nil }
func (m *memUnits) RestoreUnit(data []byte) error { m.data = data; return nil }

// testApplier is root's stacks-dir-apply on a testInstaller host whose
// rules come from testStacksRules.
func testApplier(t *testing.T) (*testInstaller, *stacksDirApplier, string, *int) {
	t.Helper()
	ti := newTestInstaller(t)
	_, rules := testStacksRules(t)
	rules.parents = append(rules.parents, ti.path("srv"))
	data := ti.path("var/agent")
	if _, err := ti.run(installOptions{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	reqDir := filepath.Join(data, "update")
	if err := os.MkdirAll(reqDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ti.checkStacks = func(path, dataDir, current string, uid int, _ map[string]string) stacksDirResult {
		r := rules
		r.dataDir, r.current, r.uid = dataDir, current, uid
		return checkStacksDir(path, r)
	}
	restarts := 0
	a := &stacksDirApplier{
		dataDir: data, reqDir: reqDir, uid: uint32(os.Getuid()), gid: os.Getgid(),
		stateDir: filepath.Join(ti.root, "state"), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		rules: func(dirs stackDirs) stacksDirRules {
			r := rules
			r.dataDir, r.current, r.stackDirs = data, dirs.current, dirs.all()
			return r
		},
		in: ti.agentInstaller, units: &memUnits{},
		restart: func(context.Context) error { restarts++; return nil },
	}
	return ti, a, reqDir, &restarts
}

func readStacksResult(t *testing.T, reqDir string) stacksDirResultMsg {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(reqDir, rootupdate.StacksDirResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var m stacksDirResultMsg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStacksDirApply(t *testing.T) {
	ti, a, reqDir, restarts := testApplier(t)
	srv := filepath.Dir(ti.path("srv/x"))
	if err := os.Chmod(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(srv, "one")
	second := filepath.Join(srv, "two")

	// A stack in the default dir keeps it writable after the switch.
	mkStack(t, filepath.Join(a.dataDir, "stacks"), "old")
	steps := []struct {
		name, path, status string
		wantEnv            []string
		wantUnit           []string
	}{
		{"symlink refused", ti.path("srv") + "/../etc", agents.StacksDirRefused, nil, nil},
		{"etc refused", "/etc/x", agents.StacksDirRefused, nil, nil},
		{"two new levels refused", filepath.Join(srv, "a", "b"), agents.StacksDirRefused, nil, nil},
		{"first switch", first, agents.StacksDirDone,
			[]string{"DOCKMESH_STACKS_DIR=" + first, "DOCKMESH_EARLIER_STACKS_DIRS=" + filepath.Join(a.dataDir, "stacks")},
			[]string{`ReadWritePaths="-` + first + `"`}},
		{"same again", first, agents.StacksDirDone, []string{"DOCKMESH_STACKS_DIR=" + first}, nil},
		{"second switch drops the empty first", second, agents.StacksDirDone,
			[]string{"DOCKMESH_STACKS_DIR=" + second, "DOCKMESH_EARLIER_STACKS_DIRS=" + filepath.Join(a.dataDir, "stacks")},
			[]string{`ReadWritePaths="-` + second + `"`}},
	}
	wantRestarts := 0
	for _, s := range steps {
		before, _ := os.ReadFile(ti.path("etc/dockmesh-agent/agent.env"))
		if err := a.run(context.Background(), s.path); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		m := readStacksResult(t, reqDir)
		if m.Status != s.status {
			t.Fatalf("%s: status %s (%s), want %s", s.name, m.Status, m.Reason, s.status)
		}
		env := ti.read(t, "etc/dockmesh-agent/agent.env")
		if s.status == agents.StacksDirRefused && env != string(before) {
			t.Errorf("%s: agent.env changed after a refusal", s.name)
		}
		for _, l := range s.wantEnv {
			if !strings.Contains(env, l+"\n") {
				t.Errorf("%s: agent.env lacks %q:\n%s", s.name, l, env)
			}
		}
		unit := ti.read(t, "units/dockmesh-agent.service")
		for _, l := range s.wantUnit {
			if !strings.Contains(unit, l+"\n") {
				t.Errorf("%s: unit lacks %q:\n%s", s.name, l, unit)
			}
		}
		if strings.Contains(s.name, "switch") {
			wantRestarts++
		}
		if *restarts != wantRestarts {
			t.Errorf("%s: %d restarts, want %d", s.name, *restarts, wantRestarts)
		}
	}
	if strings.Contains(ti.read(t, "units/dockmesh-agent.service"), first) {
		t.Error("the empty earlier dir is still writable")
	}
	fi, err := os.Stat(first)
	if err != nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("created dir: %v %v", fi, err)
	}
}

func TestStacksDirApplyRefusedWhileUpdating(t *testing.T) {
	ti, a, reqDir, restarts := testApplier(t)
	if err := os.WriteFile(filepath.Join(reqDir, rootupdate.PendingFile), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := ti.read(t, "etc/dockmesh-agent/agent.env")
	if err := a.run(context.Background(), ti.path("srv/new")); err != nil {
		t.Fatal(err)
	}
	if m := readStacksResult(t, reqDir); m.Status != agents.StacksDirRefused || !strings.Contains(m.Reason, "update") {
		t.Errorf("result %+v", m)
	}
	if ti.read(t, "etc/dockmesh-agent/agent.env") != before || *restarts != 0 {
		t.Error("a refused apply changed the agent")
	}
}

func TestStacksDirRefusalNamesThePrepareCommand(t *testing.T) {
	root, rules := testStacksRules(t)
	for _, p := range []string{root + "/opt/x/stacks", root + "/full", root + "/home/x"} {
		got := checkStacksDir(p, rules)
		want := "sudo install -d -o dockmesh-agent -g dockmesh-agent -m 0750 " + p
		if got.ok() || !strings.Contains(got.Message, want) {
			t.Errorf("%s: %q lacks %q", p, got.Message, want)
		}
	}
}

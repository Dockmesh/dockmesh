//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkStack(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "compose.yaml"), []byte("services: {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestResolveStackDirs(t *testing.T) {
	tests := []struct {
		current, earlier string
		want             stackDirs
	}{
		{"", "", stackDirs{current: "/data/stacks"}},
		{"/srv/stacks/", "", stackDirs{current: "/srv/stacks"}},
		{"relative", "", stackDirs{current: "/data/stacks"}},
		{"/srv/new", "/srv/old:/srv/new::rel:/data/stacks:/srv/old", stackDirs{current: "/srv/new", earlier: []string{"/srv/old", "/data/stacks"}}},
	}
	for _, tt := range tests {
		got := resolveStackDirs(tt.current, tt.earlier, "/data")
		if got.current != tt.want.current || strings.Join(got.earlier, ":") != strings.Join(tt.want.earlier, ":") {
			t.Errorf("resolveStackDirs(%q, %q) = %+v, want %+v", tt.current, tt.earlier, got, tt.want)
		}
	}
}

func TestStackDirsHome(t *testing.T) {
	root := t.TempDir()
	d := stackDirs{current: filepath.Join(root, "new"), earlier: []string{filepath.Join(root, "old")}}
	mkStack(t, d.earlier[0], "web")
	mkStack(t, d.earlier[0], "both")
	mkStack(t, d.current, "both")
	// A symlink of that name in current is not the stack's directory.
	if err := os.Symlink(filepath.Join(root, "old", "web"), filepath.Join(d.current, "web")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"web": d.earlier[0], "both": d.current, "fresh": d.current} {
		if got := d.home(name); got != want {
			t.Errorf("home(%q) = %s, want %s", name, got, want)
		}
	}
	rep := stacksDirReport(d)
	if len(rep) != 2 || !rep[0].Current || rep[0].Stacks != 1 || rep[1].Current || rep[1].Stacks != 2 {
		t.Errorf("report = %+v", rep)
	}
}

func TestCountStacks(t *testing.T) {
	dir := t.TempDir()
	mkStack(t, dir, "a")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if n, err := countStacks(dir); err != nil || n != 1 {
		t.Errorf("countStacks = %d, %v, want 1", n, err)
	}
	if n, err := countStacks(filepath.Join(dir, "missing")); err != nil || n != 0 {
		t.Errorf("missing dir: %d, %v", n, err)
	}
	link := filepath.Join(t.TempDir(), "stacks")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := countStacks(link); err == nil {
		t.Error("a symlinked stacks dir was followed")
	}
}

func TestWritableStackDirs(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	mkStack(t, filepath.Join(data, "stacks"), "s")
	full := func(names ...string) string {
		var out []string
		for _, n := range names {
			p := filepath.Join(root, n)
			mkStack(t, p, "s")
			out = append(out, p)
		}
		return strings.Join(out, ":")
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name             string
		current, earlier string
		want             int
		wantErr          bool
	}{
		{"default only", "", "", 0, false},
		{"current outside", filepath.Join(root, "cur"), "", 1, false},
		{"empty earlier dropped", filepath.Join(root, "cur"), empty, 1, false},
		{"earlier with stacks kept", filepath.Join(root, "cur"), full("e1") + ":" + empty, 2, false},
		{"two earlier with stacks kept", filepath.Join(root, "cur"), full("e2", "e3"), 3, false},
		{"default as earlier is no entry", filepath.Join(root, "cur"), data + "/stacks", 1, false},
		{"default as current is no entry", data + "/stacks", full("e8"), 1, false},
		{"too many", filepath.Join(root, "cur"), full("e4", "e5", "e6"), 0, true},
		{"earlier in a system tree", filepath.Join(root, "cur"), "/etc/x", 0, true},
	}
	if _, err := writableStackDirs(data, filepath.Join(root, "cur"), full("e7"), func(string) bool { return false }); err == nil {
		t.Error("an earlier dir below a directory others may change was accepted")
	}
	for _, tt := range tests {
		got, err := writableStackDirs(data, tt.current, tt.earlier, func(string) bool { return true })
		if (err != nil) != tt.wantErr || (!tt.wantErr && len(got) != tt.want) {
			t.Errorf("%s: %v, %v; want %d entries, err %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestDeleteStackInEveryDir(t *testing.T) {
	root := t.TempDir()
	d := stackDirs{current: filepath.Join(root, "new"), earlier: []string{filepath.Join(root, "old")}}
	mkStack(t, d.current, "web")
	mkStack(t, d.earlier[0], "web")
	mkStack(t, d.earlier[0], "db")
	if err := deleteStack(context.Background(), nil, d, "web"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(d.current, "web"), filepath.Join(d.earlier[0], "web")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
	if !isDir(filepath.Join(d.earlier[0], "db")) {
		t.Error("another stack was removed")
	}
}

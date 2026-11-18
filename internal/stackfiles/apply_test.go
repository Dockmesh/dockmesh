//go:build unix

package stackfiles

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func keptReason(rep Report, p string) string {
	for _, n := range rep.Kept {
		if n.Path == p {
			return n.Reason
		}
	}
	return ""
}

func TestApplyFirstDeploy(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Files: []File{
		{Path: "html", Dir: true, Mode: 0o755},
		{Path: "html/index.html", Mode: 0o644, Data: []byte("v1")},
		{Path: "bin/run.sh", Mode: 0o750, Data: []byte("#!/bin/sh")},
		{Path: "ro.conf", Mode: 0o444, Data: []byte("ro")},
	}}
	m, rep, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 3 || len(rep.Kept) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if got := read(t, filepath.Join(dir, "html/index.html")); got != "v1" {
		t.Errorf("index.html = %q", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "bin/run.sh"))
	if err != nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("run.sh mode = %v, %v", fi.Mode(), err)
	}
	if len(m.Files) != 3 {
		t.Errorf("manifest files = %v", m.Files)
	}
	if strings.Join(m.Dirs, ",") != "bin,html" {
		t.Errorf("manifest dirs = %v", m.Dirs)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "html"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temp file left: %s", e.Name())
		}
	}

	// Second deploy: the read-only file changes, index.html is identical.
	set.Files[3].Data = []byte("ro2")
	if _, rep, err = Apply(openRoot(t, dir), set, m); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "ro.conf")); got != "ro2" || len(rep.Kept) != 0 {
		t.Errorf("read-only file not replaced: %q %+v", got, rep)
	}
}

func TestApplyKeepsWhatDockmeshDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "foreign.conf"), "mine", 0o644)
	write(t, filepath.Join(dir, "same.conf"), "same", 0o600)
	if err := os.MkdirAll(filepath.Join(dir, "nginx.conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("foreign.conf", filepath.Join(dir, "link.conf")); err != nil {
		t.Fatal(err)
	}
	set := &Set{Files: []File{
		{Path: "foreign.conf", Mode: 0o644, Data: []byte("server")},
		{Path: "same.conf", Mode: 0o644, Data: []byte("same")},
		{Path: "nginx.conf", Mode: 0o644, Data: []byte("conf")},
		{Path: "link.conf", Mode: 0o644, Data: []byte("server")},
	}}
	m, rep, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "foreign.conf")); got != "mine" {
		t.Errorf("foreign file overwritten: %q", got)
	}
	if r := keptReason(rep, "foreign.conf"); !strings.Contains(r, "not written by Dockmesh") {
		t.Errorf("foreign.conf reason = %q", r)
	}
	if r := keptReason(rep, "nginx.conf"); !strings.Contains(r, "directory is in the way") {
		t.Errorf("nginx.conf reason = %q", r)
	}
	if r := keptReason(rep, "link.conf"); !strings.Contains(r, "symlink") {
		t.Errorf("link.conf reason = %q", r)
	}
	// An identical file is adopted and gets the server's mode.
	if _, ok := m.Files["same.conf"]; !ok {
		t.Error("identical file not adopted")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "same.conf")); fi.Mode().Perm() != 0o644 {
		t.Errorf("same.conf mode = %v", fi.Mode())
	}
	if _, ok := m.Files["foreign.conf"]; ok {
		t.Error("foreign file recorded")
	}
}

func TestApplyChangedOnHost(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Files: []File{{Path: "app.yml", Mode: 0o644, Data: []byte("v1")}}}
	m, _, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "app.yml"), "container wrote this", 0o644)
	set.Files[0].Data = []byte("v2")
	m2, rep, err := Apply(openRoot(t, dir), set, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "app.yml")); got != "container wrote this" {
		t.Errorf("changed file overwritten: %q", got)
	}
	if r := keptReason(rep, "app.yml"); !strings.Contains(r, "changed on the host") {
		t.Errorf("reason = %q", r)
	}
	if _, ok := m2.Files["app.yml"]; ok {
		t.Error("file the container changed stays recorded")
	}
}

func TestApplyRemoves(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Files: []File{
		{Path: "html", Dir: true, Mode: 0o755},
		{Path: "html/old.html", Mode: 0o644, Data: []byte("old")},
		{Path: "html/edited.html", Mode: 0o644, Data: []byte("e")},
		{Path: "conf/a.conf", Mode: 0o644, Data: []byte("a")},
		{Path: "data", Dir: true, Mode: 0o755},
	}}
	m, _, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Container data next to Dockmesh's files, and an edit on the host.
	write(t, filepath.Join(dir, "data", "db.sqlite"), "rows", 0o600)
	write(t, filepath.Join(dir, "html", "edited.html"), "host edit", 0o644)

	next := &Set{Files: []File{{Path: "keep.txt", Mode: 0o644, Data: []byte("k")}}}
	m2, rep, err := Apply(openRoot(t, dir), next, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "html", "old.html")); !os.IsNotExist(err) {
		t.Error("removed file still there")
	}
	if _, err := os.Lstat(filepath.Join(dir, "conf")); !os.IsNotExist(err) {
		t.Error("empty directory Dockmesh created still there")
	}
	if got := read(t, filepath.Join(dir, "data", "db.sqlite")); got != "rows" {
		t.Error("container data touched")
	}
	if got := read(t, filepath.Join(dir, "html", "edited.html")); got != "host edit" {
		t.Error("file edited on the host removed")
	}
	if rep.Removed != 2 {
		t.Errorf("removed = %d, want 2 (%+v)", rep.Removed, rep)
	}
	if r := keptReason(rep, "html/edited.html"); !strings.Contains(r, "changed on the host") {
		t.Errorf("edited reason = %q", r)
	}
	// data and html still hold files, so they stay recorded and on disk.
	if strings.Join(m2.Dirs, ",") != "data,html" {
		t.Errorf("dirs = %v", m2.Dirs)
	}
}

func TestApplyStaysInStackDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "stack")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A container replaced html with a symlink out of the stack dir.
	if err := os.Symlink(outside, filepath.Join(dir, "html")); err != nil {
		t.Fatal(err)
	}
	set := &Set{Files: []File{{Path: "html/index.html", Mode: 0o644, Data: []byte("x")}}}
	_, rep, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "index.html")); !os.IsNotExist(err) {
		t.Fatal("file written outside the stack directory")
	}
	if len(rep.Kept) != 1 {
		t.Errorf("report = %+v", rep)
	}

	// A manifest entry naming a path outside never removes anything there.
	write(t, filepath.Join(base, "victim"), "v", 0o644)
	m := &Manifest{Files: map[string]string{"../victim": sum([]byte("v"))}}
	if _, _, err := Apply(openRoot(t, dir), &Set{}, m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(base, "victim")); err != nil {
		t.Fatal("file outside the stack directory removed")
	}
}

func TestApplyRefusesBadSet(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../x", "compose.yaml", ".env", "/abs"} {
		set := &Set{Files: []File{{Path: p, Mode: 0o644, Data: []byte("x")}}}
		if _, _, err := Apply(openRoot(t, dir), set, nil); err == nil {
			t.Errorf("Apply accepted %q", p)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("refused set wrote %d entries", len(entries))
	}
}

func TestApplyLeavesReplacedEntries(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Files: []File{
		{Path: "conf/a.conf", Mode: 0o644, Data: []byte("a")},
		{Path: "pipe", Mode: 0o644, Data: []byte("p")},
	}}
	m, _, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A container replaces Dockmesh's directory with its own file and a
	// recorded file with a FIFO.
	if err := os.RemoveAll(filepath.Join(dir, "conf")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "conf"), "container file", 0o644)
	if err := os.Remove(filepath.Join(dir, "pipe")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := &Set{Files: []File{{Path: "pipe", Mode: 0o644, Data: []byte("p2")}}}
	_, rep, err := Apply(openRoot(t, dir), next, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "conf")); got != "container file" {
		t.Errorf("file at a recorded directory removed")
	}
	if r := keptReason(rep, "pipe"); !strings.Contains(r, "special file") {
		t.Errorf("pipe reason = %q", r)
	}
}

func TestApplyDoesNotReadOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	set := &Set{Files: []File{{Path: "a.conf", Mode: 0o644, Data: []byte("v1")}}}
	m, _, err := Apply(openRoot(t, dir), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse and larger than anything a set carries: never read.
	f, err := os.OpenFile(filepath.Join(dir, "a.conf"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 40); err != nil {
		t.Skip("no sparse files here:", err)
	}
	f.Close()
	_, rep, err := Apply(openRoot(t, dir), &Set{Files: []File{{Path: "a.conf", Mode: 0o644, Data: []byte("v2")}}}, m)
	if err != nil {
		t.Fatal(err)
	}
	if r := keptReason(rep, "a.conf"); !strings.Contains(r, "changed on the host") {
		t.Errorf("reason = %q", r)
	}
}

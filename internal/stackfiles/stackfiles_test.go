package stackfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func paths(s *Set) map[string]File {
	out := map[string]File{}
	for _, f := range s.Files {
		out[f.Path] = f
	}
	return out
}

func TestSkip(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"compose.yaml", true},
		{"Compose.YML", true},
		{"docker-compose.yaml", true},
		{"compose.prod.yaml", true},
		{".env", true},
		{".env.age", true},
		{".dockmesh.meta.json", true},
		{".git", true},
		{".git/config", true},
		{".gitignore", true},
		{"html/.dockmesh-tmp-abc", true},
		{"html/index.html", false},
		{"conf/compose.yaml", false},
		{"conf/.env", false},
		{"nginx.conf", false},
		{".envrc", false},
	}
	for _, c := range cases {
		if got := Skip(c.rel); got != c.want {
			t.Errorf("Skip(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		path string
		ok   bool
	}{
		{"plain", "html/index.html", true},
		{"parent", "../x", false},
		{"inner parent", "html/../../x", false},
		{"absolute", "/etc/passwd", false},
		{"dot", ".", false},
		{"empty element", "a//b", false},
		{"compose", "compose.yaml", false},
		{"env", ".env", false},
		{"meta", ".dockmesh.meta.json", false},
		{"backslash", `a\b`, false},
		{"nul", "a\x00b", false},
		{"long", strings.Repeat("a", MaxPathBytes+1), false},
		{"control", "a\nb", false},
		{"invalid utf8", "a\xffb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Set{Files: []File{{Path: c.path, Mode: 0o644, Data: []byte("x")}}}
			if err := s.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate(%q) = %v, ok want %v", c.path, err, c.ok)
			}
		})
	}
	names := &Set{Files: make([]File, 0, MaxEntries)}
	for i := 0; i < MaxEntries; i++ {
		names.Files = append(names.Files, File{Path: strconv.Itoa(i) + strings.Repeat("<", MaxPathBytes-8), Data: make([]byte, 2000)})
	}
	if err := names.Validate(); err == nil {
		t.Fatal("set over the encoded limit accepted")
	}
	dup := &Set{Files: []File{{Path: "a"}, {Path: "a"}}}
	if dup.Validate() == nil {
		t.Fatal("duplicate path accepted")
	}
	big := &Set{Files: []File{{Path: "a", Data: make([]byte, MaxTotalBytes+1)}}}
	if big.Validate() == nil {
		t.Fatal("oversized set accepted")
	}
}

func TestCollect(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "stack")
	write(t, filepath.Join(dir, "compose.yaml"), "services: {}", 0o644)
	write(t, filepath.Join(dir, ".env"), "SECRET=1", 0o600)
	write(t, filepath.Join(dir, ".dockmesh.meta.json"), "{}", 0o644)
	write(t, filepath.Join(dir, "compose.prod.yaml"), "services: {}", 0o644)
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref", 0o644)
	write(t, filepath.Join(dir, "html", "index.html"), "<h1>hi</h1>", 0o644)
	write(t, filepath.Join(dir, "bin", "run.sh"), "#!/bin/sh", 0o755)
	write(t, filepath.Join(base, "outside"), "secret", 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("html/index.html", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("html", filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.env", filepath.Join(dir, "html", "leak")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}

	set, skipped, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(set)
	for _, p := range []string{"compose.yaml", ".env", ".dockmesh.meta.json", "compose.prod.yaml", ".git", ".git/HEAD", "escape", "dirlink", "inside", "html/leak", "fifo"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s collected", p)
		}
	}
	if f := got["html/index.html"]; string(f.Data) != "<h1>hi</h1>" || f.Mode != 0o644 {
		t.Errorf("index.html = %+v", f)
	}
	if f := got["bin/run.sh"]; f.Mode != 0o755 {
		t.Errorf("run.sh mode = %o", f.Mode)
	}
	if f := got["empty"]; !f.Dir || f.Mode != 0o750 {
		t.Errorf("empty dir = %+v", f)
	}
	reasons := map[string]string{}
	for _, n := range skipped {
		reasons[n.Path] = n.Reason
	}
	for _, p := range []string{"escape", "dirlink", "inside", "html/leak"} {
		if !strings.Contains(reasons[p], "symlink") {
			t.Errorf("%s not reported as symlink: %v", p, skipped)
		}
	}
	if !strings.Contains(reasons["fifo"], "regular") {
		t.Errorf("fifo not reported: %v", skipped)
	}
}

func TestCollectUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pgdata", "PG_VERSION"), "17", 0o600)
	write(t, filepath.Join(dir, "secret.key"), "k", 0o000)
	write(t, filepath.Join(dir, "ok.txt"), "ok", 0o644)
	if err := os.Chmod(filepath.Join(dir, "pgdata"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "pgdata"), 0o755) })
	set, skipped, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(set)
	if _, ok := got["pgdata"]; ok {
		t.Error("unreadable directory collected")
	}
	if _, ok := got["secret.key"]; ok {
		t.Error("unreadable file collected")
	}
	if _, ok := got["ok.txt"]; !ok {
		t.Error("readable file missing")
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestCollectTooLarge(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", MaxTotalBytes-10), 0o644)
	write(t, filepath.Join(dir, "more.bin"), strings.Repeat("y", 100), 0o644)
	_, _, err := Collect(dir)
	var tl *TooLargeError
	if !errors.As(err, &tl) {
		t.Fatalf("err = %v, want TooLargeError", err)
	}
	if !strings.Contains(err.Error(), "big.bin") || !strings.Contains(err.Error(), "8.0 MiB") {
		t.Errorf("message lacks the largest file or the limit: %v", err)
	}

	many := t.TempDir()
	for i := 0; i <= MaxEntries; i++ {
		write(t, filepath.Join(many, "d", "f"+strconv.Itoa(i)), "", 0o644)
	}
	if _, _, err := Collect(many); !errors.As(err, &tl) || !strings.Contains(err.Error(), "more than 2000") {
		t.Fatalf("too many entries: %v", err)
	}
}

package scanner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGrype writes a script that records its source argument, and a copy
// of the archive it was given, then prints an empty report.
func fakeGrype(t *testing.T) (binary, argFile, copyFile string) {
	t.Helper()
	dir := t.TempDir()
	argFile, copyFile = filepath.Join(dir, "arg"), filepath.Join(dir, "copy")
	binary = filepath.Join(dir, "grype")
	script := "#!/bin/sh\necho \"$1\" > " + argFile + "\ncp \"${1#docker-archive:}\" " + copyFile + " 2>/dev/null\necho '{\"matches\":[],\"descriptor\":{\"version\":\"1\"}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, argFile, copyFile
}

func TestScanArchive(t *testing.T) {
	binary, argFile, copyFile := fakeGrype(t)
	dir := t.TempDir()
	g := NewGrypeCLI(binary)
	g.ArchiveDir = dir

	rep, err := g.ScanArchive(context.Background(), strings.NewReader("image-archive"), "nginx:1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Image != "nginx:1" {
		t.Fatalf("report filed under %q", rep.Image)
	}
	arg, _ := os.ReadFile(argFile)
	if !strings.HasPrefix(string(arg), "docker-archive:"+dir) {
		t.Fatalf("grype source = %q", arg)
	}
	if got, _ := os.ReadFile(copyFile); string(got) != "image-archive" {
		t.Fatalf("grype read %q", got)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("archive left behind: %v", left)
	}
}

func TestPrepareDirs(t *testing.T) {
	data := t.TempDir()
	stale := filepath.Join(data, ArchiveDirName, "dockmesh-scan-1.tar")
	db := filepath.Join(data, DBDirName, "vulnerability.db")
	for _, f := range []string{stale, db} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archiveDir, dbDir, err := PrepareDirs(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("leftover still there: %v", err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("database was removed: %v", err)
	}
	for _, dir := range []string{archiveDir, dbDir} {
		if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, %v", dir, st, err)
		}
	}
}

func TestScanAlwaysUsesTheDaemonSource(t *testing.T) {
	for _, ref := range []string{"nginx:1", "registry:2", "docker:27"} {
		binary, argFile, _ := fakeGrype(t)
		if _, err := NewGrypeCLI(binary).Scan(context.Background(), ref); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		arg, _ := os.ReadFile(argFile)
		if strings.TrimSpace(string(arg)) != "docker:"+ref {
			t.Errorf("%s: grype source = %q", ref, arg)
		}
	}
}

func TestArchiveScansRunOneAtATime(t *testing.T) {
	binary, _, _ := fakeGrype(t)
	g := NewGrypeCLI(binary)
	g.ArchiveDir = t.TempDir()
	g.archiveSlot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ScanArchive(ctx, strings.NewReader("x"), "nginx:1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(g.ArchiveDir); len(left) != 0 {
		t.Fatalf("waiting scan wrote an archive: %v", left)
	}
}

func TestCappedBuffer(t *testing.T) {
	b := cappedBuffer{max: 4}
	if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b.String() != "abcd" || !b.truncated {
		t.Fatalf("%q truncated=%v", b.String(), b.truncated)
	}
}

func TestGrypeEnv(t *testing.T) {
	t.Setenv("DOCKMESH_SECRET_TEST", "x")
	t.Setenv("GRYPE_CHECK_FOR_APP_UPDATE", "false")
	env := grypeEnv("/data/grype-db")
	if slices.Contains(env, "DOCKMESH_SECRET_TEST=x") {
		t.Fatal("server setting reached grype")
	}
	if !slices.Contains(env, "GRYPE_CHECK_FOR_APP_UPDATE=false") {
		t.Fatal("grype setting was dropped")
	}
	if !slices.Contains(env, "GRYPE_DB_CACHE_DIR=/data/grype-db") {
		t.Fatal("database directory not set")
	}
	t.Setenv("GRYPE_DB_CACHE_DIR", "/custom")
	if slices.Contains(grypeEnv("/data/grype-db"), "GRYPE_DB_CACHE_DIR=/data/grype-db") {
		t.Fatal("overrode the admin's database directory")
	}
}

func TestGrypeEnvAppUpdateCheck(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want string
		deny string
	}{
		{name: "off when unset", want: "GRYPE_CHECK_FOR_APP_UPDATE=false"},
		{name: "admin turned it on", set: "true", want: "GRYPE_CHECK_FOR_APP_UPDATE=true", deny: "GRYPE_CHECK_FOR_APP_UPDATE=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set != "" {
				t.Setenv("GRYPE_CHECK_FOR_APP_UPDATE", tt.set)
			} else {
				t.Setenv("GRYPE_CHECK_FOR_APP_UPDATE", "")
				os.Unsetenv("GRYPE_CHECK_FOR_APP_UPDATE")
			}
			env := grypeEnv("")
			if !slices.Contains(env, tt.want) {
				t.Errorf("missing %s", tt.want)
			}
			if tt.deny != "" && slices.Contains(env, tt.deny) {
				t.Error("overrode the admin's setting")
			}
		})
	}
}

func TestGrypeFailureNamesTheCause(t *testing.T) {
	got := grypeFailure("[0000]  WARN update\n[0000] ERROR \x1b[31mdatabase does not exist\x1b[0m\n")
	if got != "[0000] ERROR database does not exist" {
		t.Fatalf("got %q", got)
	}
}

func TestReservingWriterStopsWhenDiskIsLow(t *testing.T) {
	free := diskReserveBytes
	w := &reservingWriter{dst: io.Discard, free: func(string) (uint64, bool) { return free, true }}
	if _, err := w.Write(make([]byte, diskCheckBytes)); err != nil {
		t.Fatal(err)
	}
	free--
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrScanDiskFull) {
		t.Fatalf("err = %v", err)
	}
	unknown := &reservingWriter{free: func(string) (uint64, bool) { return 0, false }}
	if err := unknown.check(); err != nil {
		t.Fatalf("unknown free space must not refuse: %v", err)
	}
}

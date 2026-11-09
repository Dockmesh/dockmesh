package scanner

import (
	"context"
	"os"
	"path/filepath"
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
	script := "#!/bin/sh\necho \"$1\" > " + argFile + "\ncp \"${1#docker-archive:}\" " + copyFile + "\necho '{\"matches\":[],\"descriptor\":{\"version\":\"1\"}}'\n"
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

func TestPrepareArchiveDirClearsLeftovers(t *testing.T) {
	data := t.TempDir()
	stale := filepath.Join(data, ArchiveDirName, "dockmesh-scan-1.tar")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := PrepareArchiveDir(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("leftover still there: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("archive dir: %v, %v", st, err)
	}
}

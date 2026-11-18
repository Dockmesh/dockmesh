//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/stackfiles"
)

func TestApplyStackFiles(t *testing.T) {
	root := t.TempDir()
	dirs := stackDirs{current: filepath.Join(root, "new"), earlier: []string{filepath.Join(root, "old")}}
	mkStack(t, dirs.earlier[0], "web")
	manifests := filepath.Join(root, "data", stackFilesDir)
	set := &stackfiles.Set{Files: []stackfiles.File{
		{Path: "html/index.html", Mode: 0o644, Data: []byte("<h1>v1</h1>")},
	}}

	// Applying the same set twice changes nothing the second time.
	for i := 0; i < 2; i++ {
		rep, err := applyStackFiles(dirs, manifests, "web", set)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Copied != 1 || len(rep.Kept) != 0 {
			t.Fatalf("run %d: report = %+v", i, rep)
		}
	}
	// The stack lives in the earlier dir; its files go there.
	b, err := os.ReadFile(filepath.Join(dirs.earlier[0], "web", "html", "index.html"))
	if err != nil || string(b) != "<h1>v1</h1>" {
		t.Fatalf("index.html = %q, %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(manifests, "web.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("manifest = %v, %v", fi, err)
	}

	// Gone from the server: removed on the next deploy.
	rep, err := applyStackFiles(dirs, manifests, "web", &stackfiles.Set{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Lstat(filepath.Join(dirs.earlier[0], "web", "html")); !os.IsNotExist(err) {
		t.Error("html dir left behind")
	}

	if err := removeStackFilesManifest(manifests, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(manifests, "web.json")); !os.IsNotExist(err) {
		t.Error("manifest not removed")
	}
	if _, err := applyStackFiles(dirs, manifests, "../x", set); err == nil {
		t.Error("invalid stack name accepted")
	}
}

func TestApplyStackFilesUnreadableManifest(t *testing.T) {
	root := t.TempDir()
	dirs := stackDirs{current: filepath.Join(root, "stacks")}
	manifests := filepath.Join(root, "data", stackFilesDir)
	mkStack(t, dirs.current, "web")
	if err := os.WriteFile(filepath.Join(dirs.current, "web", "a.conf"), []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "web.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	set := &stackfiles.Set{Files: []stackfiles.File{{Path: "a.conf", Mode: 0o644, Data: []byte("server")}}}
	rep, err := applyStackFiles(dirs, manifests, "web", set)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 {
		t.Fatalf("a broken manifest must not make the agent overwrite files: %+v", rep)
	}
}

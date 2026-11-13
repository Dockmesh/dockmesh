package stacks

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteForeignOwnedWithoutDocker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove any directory, so the permission error cannot be simulated")
	}
	m, dir, _ := saveFixture(t)
	locked := filepath.Join(dir, "pgdata")
	if err := os.MkdirAll(filepath.Join(locked, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	err := m.Delete(context.Background(), "app")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Delete = %v, want a permission error", err)
	}
	if !m.Has("app") {
		t.Fatal("a failed delete forgot the stack")
	}

	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if m.Has("app") {
		t.Fatal("stack still known after delete")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stack dir still there: %v", err)
	}
}

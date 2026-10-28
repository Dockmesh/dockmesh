package stacks

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

func newSumManager(t *testing.T) *Manager {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.SetSumStore(NewSumStore(database))
	return m
}

func TestModifiedOutside(t *testing.T) {
	m := newSumManager(t)
	if _, err := m.Create("web", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("web")

	changed, err := m.ModifiedOutside("web")
	if err != nil || len(changed) != 0 {
		t.Fatalf("fresh stack: %v, %v", changed, err)
	}
	if _, err := m.Update("web", "services: {a: {image: x}}\n", ""); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
		t.Fatalf("own update reported as outside change: %v", changed)
	}

	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {evil: {image: x}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("B=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = m.ModifiedOutside("web")
	if err != nil || !reflect.DeepEqual(changed, []string{".env", "compose.yaml"}) {
		t.Fatalf("outside edit: %v, %v", changed, err)
	}

	if err := m.AcceptExternal("web"); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
		t.Errorf("after accept: %v", changed)
	}
}

func TestModifiedOutsideRecordsUntrackedStack(t *testing.T) {
	m := newSumManager(t)
	dir := filepath.Join(m.rootAbs, "legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ModifiedOutside("legacy"); err != nil || len(changed) != 0 {
		t.Fatalf("untracked stack: %v, %v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {x: {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("legacy"); len(changed) != 1 {
		t.Errorf("edit after baseline not detected: %v", changed)
	}
}

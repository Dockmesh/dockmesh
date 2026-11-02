package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

func TestCheckOverride(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetSumStore(NewSumStore(database))
	if _, err := m.Create("web", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("web")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.prod.yaml", "a")
	write("compose.dev.yaml", "b")

	steps := []struct {
		name     string
		do       func()
		override string
		ok       bool
	}{
		{"no override", func() {}, "", true},
		{"never confirmed", func() {}, "prod", false},
		{"confirmed", func() { _ = m.RecordOverride("web", "prod") }, "prod", true},
		{"other override", func() {}, "dev", false},
		{"file changed", func() { write("compose.prod.yaml", "evil") }, "prod", false},
		{"bad name", func() {}, "../x", false},
	}
	for _, st := range steps {
		st.do()
		err := m.CheckOverride("web", st.override)
		if (err == nil) != st.ok {
			t.Errorf("%s: err = %v, want ok=%v", st.name, err, st.ok)
		}
		if err != nil && st.name != "bad name" && !errors.Is(err, ErrOverrideUnconfirmed) {
			t.Errorf("%s: err = %v, want ErrOverrideUnconfirmed", st.name, err)
		}
	}
}

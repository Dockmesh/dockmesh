package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestModifiedOutsideMetaAndOverrides(t *testing.T) {
	tests := []struct {
		name   string
		before map[string]string
		change func(t *testing.T, dir string)
		want   []string
	}{
		{"meta file added", nil, func(t *testing.T, dir string) { writeFile(t, dir, MetaFileName, `{"scaling":{}}`) }, []string{MetaFileName}},
		{"meta file changed", map[string]string{MetaFileName: `{}`}, func(t *testing.T, dir string) { writeFile(t, dir, MetaFileName, `{"active_environment":"x"}`) }, []string{MetaFileName}},
		{"meta file removed", map[string]string{MetaFileName: `{}`}, func(t *testing.T, dir string) { removeFile(t, dir, MetaFileName) }, []string{MetaFileName}},
		{"override added", nil, func(t *testing.T, dir string) { writeFile(t, dir, "compose.x.yaml", "services: {}\n") }, []string{"compose.x.yaml"}},
		{"override changed", map[string]string{"compose.prod.yaml": "a: 1\n"}, func(t *testing.T, dir string) { writeFile(t, dir, "compose.prod.yaml", "a: 2\n") }, []string{"compose.prod.yaml"}},
		{"override removed", map[string]string{"compose.prod.yaml": "a: 1\n"}, func(t *testing.T, dir string) { removeFile(t, dir, "compose.prod.yaml") }, []string{"compose.prod.yaml"}},
		{"other file", nil, func(t *testing.T, dir string) { writeFile(t, dir, "notes.txt", "x") }, nil},
		{"override name not an environment", nil, func(t *testing.T, dir string) { writeFile(t, dir, "compose.Prod.yaml", "x") }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSumManager(t)
			if _, err := m.Create("web", "services: {}\n", "A=1\n"); err != nil {
				t.Fatal(err)
			}
			dir, _ := m.Dir("web")
			for f, c := range tt.before {
				writeFile(t, dir, f, c)
			}
			if err := m.recordSums("web"); err != nil {
				t.Fatal(err)
			}
			tt.change(t, dir)
			changed, err := m.ModifiedOutside("web")
			if err != nil || !slices.Equal(changed, tt.want) {
				t.Fatalf("changed = %v, %v; want %v", changed, err, tt.want)
			}
			if err := m.AcceptExternal(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
				t.Errorf("after accept: %v", changed)
			}
		})
	}
}

func TestBackfillRecordsMissingRowsOnce(t *testing.T) {
	m := newSumManager(t)
	for _, name := range []string{"old", "new"} {
		if _, err := m.Create(name, "services: {}\n", "A=1\n"); err != nil {
			t.Fatal(err)
		}
	}
	oldDir, _ := m.Dir("old")
	newDir, _ := m.Dir("new")
	// What the backfill migration leaves for a stack tracked before the
	// upgrade: rows for compose.yaml and .env plus the marker.
	if err := m.sums.set("old", backfillRow, ""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, oldDir, MetaFileName, `{"active_environment":"prod"}`)
	writeFile(t, oldDir, "compose.prod.yaml", "a: 1\n")
	writeFile(t, oldDir, ".env", "CHANGED=1\n")
	writeFile(t, newDir, "compose.x.yaml", "a: 1\n")

	m.RecordUntrackedSums()

	if changed, _ := m.ModifiedOutside("old"); !slices.Equal(changed, []string{".env"}) {
		t.Errorf("old: changed = %v, want only the .env changed before the upgrade", changed)
	}
	if changed, _ := m.ModifiedOutside("new"); !slices.Equal(changed, []string{"compose.x.yaml"}) {
		t.Errorf("new: changed = %v, a stack without the marker must not be backfilled", changed)
	}
	recorded, err := m.sums.get("old")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded[backfillRow]; ok {
		t.Error("marker not removed")
	}

	writeFile(t, oldDir, "compose.y.yaml", "a: 1\n")
	m.RecordUntrackedSums()
	if changed, _ := m.ModifiedOutside("old"); !slices.Contains(changed, "compose.y.yaml") {
		t.Errorf("second start accepted a new override file: %v", changed)
	}
}

func TestWriteFileKeepsOutsideChange(t *testing.T) {
	m := newSumManager(t)
	if _, err := m.Create("web", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("web")
	write := func(content string) func() error {
		return func() error { return os.WriteFile(filepath.Join(dir, MetaFileName), []byte(content), 0o644) }
	}
	if err := m.WriteFile("web", MetaFileName, write(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
		t.Fatalf("own write reported: %v", changed)
	}
	writeFile(t, dir, MetaFileName, `{"evil":1}`)
	if err := m.WriteFile("web", MetaFileName, write(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); !slices.Equal(changed, []string{MetaFileName}) {
		t.Errorf("write over an outside change confirmed it: %v", changed)
	}
	if err := m.WriteFile("web", "notes.txt", write("")); err == nil {
		t.Error("untracked file accepted")
	}
	failing := errors.New("boom")
	if err := m.WriteFile("web", MetaFileName, func() error { return failing }); !errors.Is(err, failing) {
		t.Errorf("err = %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

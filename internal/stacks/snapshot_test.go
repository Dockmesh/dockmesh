package stacks

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestSnapshotAcceptRecordsWhatWasRead(t *testing.T) {
	m := newSumManager(t)
	if _, err := m.Create("web", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("web")
	writeFile(t, dir, "compose.yaml", "services: {a: {image: x}}\n")
	snap, err := m.Snapshot("web")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(snap.Changed, []string{"compose.yaml"}) || snap.Detail.Compose != "services: {a: {image: x}}\n" || snap.Detail.Env != "A=1\n" {
		t.Fatalf("snapshot = %+v %+v", snap, snap.Detail)
	}
	// Swapped after the read: accepting the snapshot must not accept this.
	writeFile(t, dir, "compose.yaml", "services: {a: {image: x, privileged: true}}\n")
	if err := m.AcceptSnapshot(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); !slices.Equal(changed, []string{"compose.yaml"}) {
		t.Errorf("changed after accept = %v, want the swapped compose.yaml", changed)
	}
}

func TestNonRegularAndOversizeFilesCountAsChanged(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"fifo", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o644); err != nil {
				t.Skip(err)
			}
		}},
		{"oversize", func(t *testing.T, path string) {
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.Truncate(maxTrackedSize + 1); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSumManager(t)
			if _, err := m.Create("web", "services: {}\n", ""); err != nil {
				t.Fatal(err)
			}
			dir, _ := m.Dir("web")
			tt.plant(t, filepath.Join(dir, "compose.x.yaml"))
			for i := 0; i < 2; i++ {
				changed, err := m.ModifiedOutside("web")
				if err != nil || !slices.Equal(changed, []string{"compose.x.yaml"}) {
					t.Fatalf("check %d: %v, %v", i, changed, err)
				}
				if err := m.AcceptExternal(t.Context(), "web"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

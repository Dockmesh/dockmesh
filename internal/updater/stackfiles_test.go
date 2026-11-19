package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/api/types/container"
)

func TestStackTargetRefusesOutsideChanges(t *testing.T) {
	tests := []struct {
		name, file string
		wantErr    bool
	}{
		{"unchanged", "", false},
		{"compose.yaml", "compose.yaml", true},
		{"meta file", stacks.MetaFileName, true},
		{"override file", "compose.prod.yaml", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			m, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { m.Close() })
			m.SetSumStore(stacks.NewSumStore(database))
			if _, err := m.Create("app", "services:\n  web:\n    image: nginx:1\n", ""); err != nil {
				t.Fatal(err)
			}
			if tt.file != "" {
				dir, _ := m.Dir("app")
				if err := os.WriteFile(filepath.Join(dir, tt.file), []byte("services:\n  web:\n    image: nginx:1\n    privileged: true\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{}
			s.SetStacks(m, nil)
			info := &container.InspectResponse{Config: &container.Config{Labels: map[string]string{
				compose.LabelProject: "app", compose.LabelService: "web",
			}}}
			st, err := s.stackTarget(context.Background(), info)
			if tt.wantErr {
				if !errors.Is(err, stacks.ErrModifiedOutside) {
					t.Fatalf("err = %v, want ErrModifiedOutside", err)
				}
				return
			}
			if err != nil || st == nil || st.svc.Name != "web" {
				t.Fatalf("stackTarget = %+v, %v", st, err)
			}
		})
	}
}

// A confirmation covers the files the user was shown: stackTarget leaves
// it for accept, and a file changed again since is refused.
func TestStackTargetTakesConfirmation(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	m, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.SetSumStore(stacks.NewSumStore(database))
	if _, err := m.Create("app", "services:\n  web:\n    image: nginx:1\n", ""); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("app")
	write := func(image string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: "+image+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nginx:2")
	shown, err := m.Snapshot("app")
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	ctx := stacks.WithConfirmation(context.Background(), &stacks.Confirmation{Snap: shown, Accept: func(ctx context.Context) error {
		accepted++
		return m.AcceptSnapshot(ctx, shown)
	}})
	s := &Service{}
	s.SetStacks(m, nil)
	info := &container.InspectResponse{Config: &container.Config{Labels: map[string]string{
		compose.LabelProject: "app", compose.LabelService: "web",
	}}}

	st, err := s.stackTarget(ctx, info)
	if err != nil || st.svc.Image != "nginx:2" {
		t.Fatalf("stackTarget = %+v, %v", st, err)
	}
	if accepted != 0 {
		t.Fatal("confirmation recorded before the stack was locked")
	}
	if err := st.accept(ctx); err != nil || accepted != 1 {
		t.Fatalf("accept: %v, %d calls", err, accepted)
	}

	write("nginx:3")
	if _, err := s.stackTarget(ctx, info); !errors.Is(err, stacks.ErrModifiedOutside) {
		t.Fatalf("file changed after the confirmation: err = %v", err)
	}
}

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/stacks"
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
			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
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

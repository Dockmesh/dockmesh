package migration

import (
	"context"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestNamedVolumes(t *testing.T) {
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	s := &Service{stacks: mgr}

	tests := []struct {
		stack, compose string
		want           []string
	}{
		{"plain", "services:\n  web:\n    image: nginx\n", []string{}},
		{"bindonly", "services:\n  web:\n    image: nginx\n    volumes:\n      - ./html:/usr/share/nginx/html\n", []string{}},
		{"named", "services:\n  db:\n    image: postgres\n    volumes:\n      - pgdata:/var/lib/postgresql/data\nvolumes:\n  pgdata: {}\n", []string{"named_pgdata"}},
		{"explicit", "services:\n  db:\n    image: postgres\n    volumes:\n      - data:/data\nvolumes:\n  data:\n    name: shared-data\n", []string{"shared-data"}},
	}
	for _, tt := range tests {
		t.Run(tt.stack, func(t *testing.T) {
			if _, err := mgr.Create(tt.stack, tt.compose, ""); err != nil {
				t.Fatal(err)
			}
			got, err := s.namedVolumes(context.Background(), tt.stack)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

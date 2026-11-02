package stacks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

func TestHistoryRecordsEnvironment(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	s := NewHistoryStore(database)
	ctx := context.Background()
	base := "services:\n  db:\n    environment:\n      PASSWORD: ${DB_PASSWORD}\n"

	plainID, err := s.Record(ctx, "web", "local", base, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	envID, err := s.RecordEnvironment(ctx, "web", "local", base, "prod", "env: prod", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		id      int64
		wantEnv string
	}{
		{plainID, ""},
		{envID, "prod"},
	}
	for _, tt := range tests {
		e, err := s.Get(ctx, "web", tt.id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Environment != tt.wantEnv || e.ComposeYAML != base {
			t.Errorf("entry %d: environment %q compose %q", tt.id, e.Environment, e.ComposeYAML)
		}
	}
	list, err := s.List(ctx, "web", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Environment != "prod" || list[1].Environment != "" {
		t.Errorf("list = %+v", list)
	}
}

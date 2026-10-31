package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

// absPlaceholder is replaced by an absolute path on the test's platform.
const absPlaceholder = "<abs>"

func TestMigrateSystemJobPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		archives bool
		wantTo   func(cwd, dataDir string) string
	}{
		{name: "relative, archives in the old directory", path: "./data/backups", archives: true,
			wantTo: func(cwd, _ string) string { return filepath.Join(cwd, "data", "backups") }},
		{name: "relative, no archives", path: "./data/backups",
			wantTo: func(_, dataDir string) string { return filepath.Join(dataDir, "backups") }},
		{name: "empty path means the old default", path: "",
			wantTo: func(_, dataDir string) string { return filepath.Join(dataDir, "backups") }},
		{name: "absolute path is left alone", path: absPlaceholder,
			wantTo: func(_, _ string) string { return "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cwd := t.TempDir()
			t.Chdir(cwd)
			dataDir := t.TempDir()
			if tt.path == absPlaceholder {
				tt.path = filepath.Join(cwd, "elsewhere")
			}
			database, err := db.Open(filepath.Join(dataDir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			svc := NewService(database, nil, nil, nil, SystemPaths{DBPath: filepath.Join(dataDir, "test.db"), StacksRoot: t.TempDir(), DataDir: dataDir})
			id, err := svc.store.createJob(ctx, JobInput{
				Name: DefaultSystemJobName, TargetType: "local",
				TargetConfig: map[string]any{"path": tt.path},
				Sources:      []Source{{Type: "system", Name: "dockmesh"}},
				Schedule:     DefaultSystemSchedule, RetentionDays: 14, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.archives {
				dir := filepath.Join(cwd, "data", "backups", "jobs", DefaultSystemJobName)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "20261001-030000-r1.tar.gz"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, to, err := svc.MigrateSystemJobPath(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.wantTo(cwd, dataDir)
			if to != want {
				t.Fatalf("to = %q, want %q", to, want)
			}
			j, err := svc.store.getJob(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			stored, _ := j.TargetConfig.(map[string]any)["path"].(string)
			if want == "" {
				want = tt.path
			}
			if stored != want {
				t.Errorf("stored path = %q, want %q", stored, want)
			}
			if !j.Enabled || j.Schedule != DefaultSystemSchedule || j.RetentionDays != 14 {
				t.Errorf("other job fields changed: %+v", j)
			}

			if _, to, err := svc.MigrateSystemJobPath(ctx); err != nil || to != "" {
				t.Errorf("second run changed the job again: to=%q err=%v", to, err)
			}
		})
	}
}

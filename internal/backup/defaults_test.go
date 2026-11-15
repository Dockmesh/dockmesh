package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
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
			database, err := dbtest.Open(filepath.Join(dataDir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
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

func TestMarkAutoCreatedJobsForReview(t *testing.T) {
	const (
		appliedAt     = "2026-05-03 10:00:00"
		createdBefore = "2026-04-20 03:00:00"
	)
	tests := []struct {
		name     string
		create   func(t *testing.T, svc *Service) int64
		flagged  bool
		backdate bool
	}{
		{name: "created by the system backup switch", create: func(t *testing.T, svc *Service) int64 {
			if err := svc.SetDefaultJobEnabled(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			return systemJobID(t, svc)
		}},
		{name: "created in the job wizard", create: func(t *testing.T, svc *Service) int64 {
			j, err := svc.CreateJob(context.Background(), JobInput{
				Name: DefaultSystemJobName, TargetType: "local",
				TargetConfig: map[string]any{"path": filepath.Join(svc.paths.DataDir, "backups")},
				Sources:      []Source{{Type: "system", Name: "*"}},
				Schedule:     DefaultSystemSchedule, RetentionDays: 7, Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			return j.ID
		}},
		{name: "created on boot before the review migration", backdate: true, flagged: true, create: func(t *testing.T, svc *Service) int64 {
			if err := svc.SetDefaultJobEnabled(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			return systemJobID(t, svc)
		}},
		{name: "flagged by an earlier version although created later", create: func(t *testing.T, svc *Service) int64 {
			if err := svc.SetDefaultJobEnabled(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			id := systemJobID(t, svc)
			if err := svc.store.markJobNeedsReview(context.Background(), id, "Auto-created by an earlier dockmesh version."); err != nil {
				t.Fatal(err)
			}
			return id
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir := t.TempDir()
			targets.SetLocalRoots(func() []string { return []string{dataDir} })
			t.Cleanup(func() { targets.SetLocalRoots(nil) })
			database, err := dbtest.Open(filepath.Join(dataDir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if _, err := database.Exec(`UPDATE schema_migrations SET applied_at = ? WHERE version = ?`, appliedAt, reviewMigration); err != nil {
				t.Fatal(err)
			}
			svc := NewService(database, nil, nil, nil, SystemPaths{DBPath: filepath.Join(dataDir, "test.db"), StacksRoot: t.TempDir(), DataDir: dataDir})
			id := tt.create(t, svc)
			if tt.backdate {
				if _, err := database.Exec(`UPDATE backup_jobs SET created_at = ? WHERE id = ?`, createdBefore, id); err != nil {
					t.Fatal(err)
				}
			}

			for start := 1; start <= 2; start++ {
				if err := svc.MarkAutoCreatedJobsForReview(ctx); err != nil {
					t.Fatal(err)
				}
				j, err := svc.store.getJob(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if j.NeedsReview != tt.flagged || j.ReviewAcked || !j.Enabled {
					t.Fatalf("start %d: needs_review=%v acked=%v enabled=%v, want needs_review=%v", start, j.NeedsReview, j.ReviewAcked, j.Enabled, tt.flagged)
				}
			}
			if !tt.flagged {
				return
			}
			if err := svc.AcknowledgeReview(ctx, id, "keep"); err != nil {
				t.Fatal(err)
			}
			if err := svc.MarkAutoCreatedJobsForReview(ctx); err != nil {
				t.Fatal(err)
			}
			if j, err := svc.store.getJob(ctx, id); err != nil || j.NeedsReview {
				t.Fatalf("flagged again after Keep: %v %+v", err, j)
			}
		})
	}
}

func systemJobID(t *testing.T, svc *Service) int64 {
	t.Helper()
	jobs, err := svc.store.listJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j := systemJob(jobs)
	if j == nil {
		t.Fatal("no system job")
	}
	return j.ID
}

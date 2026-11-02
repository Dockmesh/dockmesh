package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
)

func TestBackupGauges(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO backup_jobs (id, name, target_type) VALUES (1, 'nightly', 'local'), (2, 'never', 'local')`); err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		status  string
		started time.Time
		end     time.Time
	}{
		{"success", finished.Add(-time.Hour), finished.Add(-time.Hour + 10*time.Second)},
		{"success_with_warnings", finished.Add(-90 * time.Second), finished},
		{"failed", finished.Add(time.Hour), finished.Add(time.Hour + time.Second)},
	} {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at)
			VALUES (1, 'nightly', ?, ?, ?)`, r.status, r.started, r.end); err != nil {
			t.Fatal(err)
		}
	}

	m := NewPromMetrics(database)
	if err := m.refreshBackups(ctx); err != nil {
		t.Fatal(err)
	}
	got := gaugeValues(t, m, "dockmesh_backup_last_run_timestamp_seconds")
	if len(got) != 1 || got["nightly"] != float64(finished.Unix()) {
		t.Errorf("last run timestamps = %v, want only nightly = %d", got, finished.Unix())
	}
	got = gaugeValues(t, m, "dockmesh_backup_last_duration_seconds")
	if len(got) != 1 || got["nightly"] != 90 {
		t.Errorf("last durations = %v, want only nightly = 90", got)
	}
}

// gaugeValues returns the samples of the gauge family name by job label.
func gaugeValues(t *testing.T, m *PromMetrics, name string) map[string]float64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "job" {
					out[l.GetValue()] = metric.GetGauge().GetValue()
				}
			}
		}
	}
	return out
}

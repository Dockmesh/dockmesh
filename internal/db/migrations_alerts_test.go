package db

import (
	"path/filepath"
	"testing"
)

// Existing CPU rules keep their per-core meaning, everything else gets no
// unit, and only installs that already have users are told about it.
func TestAlertHistoryRetentionMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		withUser   bool
		wantNotice int
	}{
		{"upgrade", true, 1},
		{"fresh install", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := migrateUpTo(database, "20261101171127_metrics_host_id.sql"); err != nil {
				t.Fatal(err)
			}
			if tt.withUser {
				if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'admin', 'x')`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.Exec(`INSERT INTO alert_rules (name, kind, container_filter, metric, operator, threshold)
				VALUES ('mine', 'metric', 'web', 'cpu_percent', 'gt', 80)`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message)
				VALUES (1, 'r', 'web', 'fired', '')`); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(database); err != nil {
				t.Fatal(err)
			}

			rows, err := database.Query(`SELECT name, kind, metric, cpu_unit FROM alert_rules`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var name, kind, metric, unit string
				if err := rows.Scan(&name, &kind, &metric, &unit); err != nil {
					t.Fatal(err)
				}
				want := ""
				if kind == "metric" && metric == "cpu_percent" {
					want = "core"
					n++
				}
				if unit != want {
					t.Errorf("rule %q (%s/%s): cpu_unit %q, want %q", name, kind, metric, unit, want)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			// The two built-in CPU rules plus ours.
			if n != 3 {
				t.Errorf("%d CPU rules, want 3", n)
			}
			var repeats int
			if err := database.QueryRow(`SELECT repeats FROM alert_history`).Scan(&repeats); err != nil || repeats != 0 {
				t.Errorf("history repeats = %d, %v", repeats, err)
			}
			var notices int
			if err := database.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = 'alerts.cpu-unit'`).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if notices != tt.wantNotice {
				t.Errorf("notices = %d, want %d", notices, tt.wantNotice)
			}
		})
	}
}

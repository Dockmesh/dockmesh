package db

import (
	"path/filepath"
	"testing"
)

// Existing runs get the host of their job; runs of a job that is gone
// count as taken on the server.
func TestBackupRunHostMigration(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261102135820_sso_immutable_subjects.sql"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id) VALUES (1, 'server', 'local', ''), (2, 'agent', 'local', 'agent-1')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status) VALUES (10, 1, 'server', 'success'), (11, 2, 'agent', 'success'), (12, 99, 'gone', 'success')`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{10: "local", 11: "agent-1", 12: "local"}
	rows, err := database.Query(`SELECT id, host_id FROM backup_runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hostID string
		if err := rows.Scan(&id, &hostID); err != nil {
			t.Fatal(err)
		}
		if hostID != want[id] {
			t.Errorf("run %d: host %q, want %q", id, hostID, want[id])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

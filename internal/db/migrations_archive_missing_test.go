package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Runs from before the archive-missing columns start unmarked, pruned
// runs stay pruned, and a second start changes nothing.
func TestBackupRunArchiveMissingColumns(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261120180816_swarm_node_proof.sql"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (1, 'db', 'local', 'local', '[]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sources_json, pruned_at) VALUES
			(1, 1, 'db', 'local', 'success', 'jobs/1-db/r1.tar.gz', '[]', '2026-01-01 00:00:00'),
			(2, 1, 'db', 'local', 'success', 'jobs/1-db/r2.tar.gz', '[]', NULL)`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"1": "pruned unmarked", "2": "kept unmarked"}
	q := `SELECT id, CASE WHEN pruned_at IS NULL THEN 'kept' ELSE 'pruned' END || ' ' ||
		CASE WHEN archive_missing_at IS NULL AND archive_missing_reason IS NULL THEN 'unmarked' ELSE 'marked' END
		FROM backup_runs`
	for start := 1; start <= 2; start++ {
		if err := Migrate(database); err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		if got := queryMap(t, database, q); !reflect.DeepEqual(got, want) {
			t.Errorf("start %d: runs = %v, want %v", start, got, want)
		}
	}
}

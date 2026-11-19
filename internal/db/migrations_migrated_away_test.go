package db

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// The pseudo rows old migrations wrote for the source host leave
// stack_deployments; real deployments and the migration history stay, and
// a second start changes nothing.
func TestDropMigratedAwaySourceRows(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261117201211_agent_setup.sql"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO stack_deployments (stack_name, host_id, status) VALUES
			('web', 'agent-1', 'deployed'),
			('web__source', 'local', 'migrated_away'),
			('db', 'local', 'stopped'),
			('db__source', 'agent-2', 'migrated_away')`,
		`INSERT INTO migrations (id, stack_name, source_host_id, target_host_id, status, phase, initiated_by) VALUES
			('m1', 'web', 'local', 'agent-1', 'completed', 'done', 'u1'),
			('m2', 'db', 'agent-2', 'local', 'completed', 'done', 'u1')`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	wantDeps := map[string]string{"web": "agent-1/deployed", "db": "local/stopped"}
	wantHistory := map[string]string{"m1": "web local>agent-1", "m2": "db agent-2>local"}
	for start := 1; start <= 2; start++ {
		if err := Migrate(database); err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		if got := queryMap(t, database, `SELECT stack_name, host_id || '/' || status FROM stack_deployments`); !reflect.DeepEqual(got, wantDeps) {
			t.Errorf("start %d: deployments = %v, want %v", start, got, wantDeps)
		}
		if got := queryMap(t, database, `SELECT id, stack_name || ' ' || source_host_id || '>' || target_host_id FROM migrations`); !reflect.DeepEqual(got, wantHistory) {
			t.Errorf("start %d: migrations = %v, want %v", start, got, wantHistory)
		}
	}
}

func queryMap(t *testing.T, database *sql.DB, q string) map[string]string {
	t.Helper()
	rows, err := database.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

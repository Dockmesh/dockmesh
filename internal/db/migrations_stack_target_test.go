package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Every deployment row becomes a host target with its own host id; the
// status, timestamps, pending delete and the migration and deploy history
// stay as they were, and a second start changes nothing.
func TestStackDeploymentTarget(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261121145822_backup_run_archive_missing.sql"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO stack_deployments (stack_name, host_id, status, deployed_at, updated_at, delete_stop) VALUES
			('web', 'agent-1', 'deployed', '2026-01-02 03:04:05', '2026-01-03 03:04:05', 0),
			('db', 'local', 'stopped', '2026-02-02 03:04:05', '2026-02-03 03:04:05', 0),
			('gone', 'agent-2', 'delete_pending', '2026-03-02 03:04:05', '2026-03-03 03:04:05', 1),
			('moved', 'agent-1', 'migrated_away', '2026-04-02 03:04:05', '2026-04-03 03:04:05', 0)`,
		`INSERT INTO migrations (id, stack_name, source_host_id, target_host_id, status, phase, initiated_by) VALUES
			('m1', 'web', 'local', 'agent-1', 'completed', 'done', 'u1')`,
		`INSERT INTO stack_deploy_history (stack_name, host_id, compose_yaml, note) VALUES
			('web', 'agent-1', 'services: {}', 'first')`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	wantDeps := map[string]string{
		"web":   "host/agent-1/agent-1/deployed/2026-01-02T03:04:05Z/2026-01-03T03:04:05Z/0",
		"db":    "host/local/local/stopped/2026-02-02T03:04:05Z/2026-02-03T03:04:05Z/0",
		"gone":  "host/agent-2/agent-2/delete_pending/2026-03-02T03:04:05Z/2026-03-03T03:04:05Z/1",
		"moved": "host/agent-1/agent-1/migrated_away/2026-04-02T03:04:05Z/2026-04-03T03:04:05Z/0",
	}
	const depsQuery = `SELECT stack_name, target_kind || '/' || target_id || '/' || host_id || '/' || status || '/' ||
		strftime('%Y-%m-%dT%H:%M:%SZ', deployed_at) || '/' || strftime('%Y-%m-%dT%H:%M:%SZ', updated_at) || '/' || delete_stop
		FROM stack_deployments`
	for start := 1; start <= 2; start++ {
		if err := Migrate(database); err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		if got := queryMap(t, database, depsQuery); !reflect.DeepEqual(got, wantDeps) {
			t.Errorf("start %d: deployments = %v, want %v", start, got, wantDeps)
		}
		if got := queryMap(t, database, `SELECT id, stack_name || ' ' || source_host_id || '>' || target_host_id FROM migrations`); !reflect.DeepEqual(got, map[string]string{"m1": "web local>agent-1"}) {
			t.Errorf("start %d: migrations = %v", start, got)
		}
		if got := queryMap(t, database, `SELECT stack_name, host_id || '/' || note FROM stack_deploy_history`); !reflect.DeepEqual(got, map[string]string{"web": "agent-1/first"}) {
			t.Errorf("start %d: deploy history = %v", start, got)
		}
	}
}

// The new table keeps one target per stack and never lets a host target
// point at another host or a cluster target carry a host.
func TestStackDeploymentTargetConstraints(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('web', 'host', 'agent-1', 'agent-1')`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, stmt string
		ok         bool
	}{
		{"cluster target without host", `INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('swarm', 'cluster', 'c1', '')`, true},
		{"second target for a stack", `INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('web', 'host', 'agent-2', 'agent-2')`, false},
		{"host target on another host", `INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('a', 'host', 'agent-1', 'agent-2')`, false},
		{"cluster target with a host", `INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('b', 'cluster', 'c1', 'agent-1')`, false},
		{"unknown kind", `INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('c', 'group', 'g', '')`, false},
	}
	for _, tc := range cases {
		_, err := database.Exec(tc.stmt)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

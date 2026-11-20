package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// dumpTables returns every row of the tables, ordered, as text.
func dumpTables(t *testing.T, database *sql.DB, tables ...string) string {
	t.Helper()
	var b strings.Builder
	for _, tbl := range tables {
		rows, err := database.Query(`SELECT * FROM ` + tbl + ` ORDER BY 1, 2`)
		if err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %v\n", tbl, cols)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  %v\n", vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return b.String()
}

// The rebuild for the cluster scope keeps every assignment (ids and the
// id counter included), token restriction and IdP mapping as it was, also
// on a second start, and accepts cluster scopes afterwards.
func TestClusterScopeMigration(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261120001120_swarm_clusters.sql"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'alice', 'x'), ('u2', 'bob', 'x')`)
	exec(`INSERT INTO role_assignments (user_id, role_name, scope_type, scope_host, scope_value, source, via, created_by, created_at) VALUES
		('u1', 'admin',    'global',   '',        '',     'manual', '',      'u1', '2026-01-02 03:04:05'),
		('u1', 'viewer',   'host',     'agent-1', '',     'manual', '',      'u1', '2026-01-02 03:04:06'),
		('u2', 'operator', 'host_tag', '',        'prod', 'idp',    'ops',   NULL, '2026-01-02 03:04:07'),
		('u2', 'deployer', 'stack',    'agent-2', 'web',  'manual', '',      'u1', '2026-01-02 03:04:08'),
		('u2', 'deployer', 'stack',    '',        'db',   'idp',    'devs',  NULL, '2026-01-02 03:04:09'),
		('u2', 'viewer',   'global',   '',        '',     'manual', '',      'u1', '2026-01-02 03:04:10')`)
	exec(`DELETE FROM role_assignments WHERE created_at = '2026-01-02 03:04:10'`)
	exec(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id, scope_type, scope_host, scope_value) VALUES
		('dmt_a', 'h1', 'ci', 'deployer', 'u1', 'stack', 'agent-2', 'web'),
		('dmt_b', 'h2', 'scrape', 'viewer', 'u2', 'host_tag', '', 'prod'),
		('dmt_c', 'h3', 'all', '', 'u1', 'global', '', '')`)
	exec(`INSERT INTO oidc_providers (id, slug, display_name, issuer_url, client_id, client_secret, default_role)
		VALUES (1, 'corp', 'Corp', 'https://idp.test', 'c', 's', 'viewer')`)
	exec(`INSERT INTO oidc_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host, scope_value) VALUES
		(1, 'ops', 'operator', 'host_tag', '', 'prod'), (1, 'devs', 'deployer', 'stack', '', 'db')`)
	exec(`INSERT INTO invite_tokens (token_hash, role, created_by, expires_at, scopes_json) VALUES
		('ih1', 'viewer', 'u1', '2030-01-01 00:00:00', '[{"type":"host","host":"agent-1"}]')`)

	tables := []string{"role_assignments", "api_tokens", "oidc_provider_group_mappings", "invite_tokens", "sqlite_sequence"}
	before := dumpTables(t, database, tables...)
	for start := range 2 {
		if err := Migrate(database); err != nil {
			t.Fatal(err)
		}
		if after := dumpTables(t, database, tables...); after != before {
			t.Fatalf("start %d changed the data:\n%s\nwant:\n%s", start, after, before)
		}
	}

	var id int64
	if err := database.QueryRow(`INSERT INTO role_assignments (user_id, role_name, scope_type, scope_host) VALUES ('u1', 'viewer', 'cluster', 'c1') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 7 {
		t.Fatalf("new assignment id %d, want 7 (the counter was not kept)", id)
	}
	if _, err := database.Exec(`INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'viewer', 'swarm')`); err == nil {
		t.Fatal("unknown scope type accepted")
	}
	var idx int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_role_assignments_role'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("role index: %d %v", idx, err)
	}
	exec(`DELETE FROM users WHERE id = 'u2'`)
	var left int
	if err := database.QueryRow(`SELECT COUNT(*) FROM role_assignments WHERE user_id = 'u2'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("cascade on user delete lost: %d %v", left, err)
	}
}

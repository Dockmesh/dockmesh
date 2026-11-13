package db

import (
	"path/filepath"
	"testing"
)

// Routes that exist at the upgrade were loaded by the old version, so they
// get the marker the proxy service turns into the hash of their state;
// routes created later start without one.
func TestProxyAppliedHashMigration(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261110124842_stack_file_sums_backfill.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO proxy_routes (host, upstream, tls_mode, enabled, options) VALUES
		('a.example.com', '10.0.0.1:80', 'auto', 1, '{}'), ('b.example.com', '10.0.0.2:80', 'none', 0, '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO proxy_routes (host, upstream) VALUES ('c.example.com', '10.0.0.3:80')`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.example.com": "@backfill", "b.example.com": "@backfill", "c.example.com": ""}
	rows, err := database.Query(`SELECT host, applied_hash FROM proxy_routes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var host, hash string
		if err := rows.Scan(&host, &hash); err != nil {
			t.Fatal(err)
		}
		if hash != want[host] {
			t.Errorf("%s: applied_hash = %q, want %q", host, hash, want[host])
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(want) {
		t.Fatalf("%d rows, want %d", n, len(want))
	}
}

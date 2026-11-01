package db

import (
	"path/filepath"
	"testing"
)

// Upgrading keeps every stored sample and files it under the server host.
func TestMetricsHostIDMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261101151453_rbac_assignments.sql"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"metrics_raw", "metrics_1m", "metrics_1h"} {
		for _, ts := range []int64{100, 200} {
			if _, err := database.Exec(`INSERT INTO `+table+`
				(container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
				VALUES ('web', ?, 150, 10, 20, 1, 2, 3, 4)`, ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"metrics_raw", "metrics_1m", "metrics_1h"} {
		var n, local int
		var cpu float64
		if err := database.QueryRow(`SELECT COUNT(*), SUM(host_id = 'local'), MAX(cpu_percent) FROM `+table).
			Scan(&n, &local, &cpu); err != nil {
			t.Fatal(err)
		}
		if n != 2 || local != 2 || cpu != 150 {
			t.Errorf("%s: rows=%d local=%d cpu=%v, want 2 rows on local with cpu 150", table, n, local, cpu)
		}
		// Same container name and time on another host is a separate row.
		if _, err := database.Exec(`INSERT INTO ` + table + `
			(host_id, container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
			VALUES ('agent-1', 'web', 100, 1, 1, 1, 1, 1, 1, 1)`); err != nil {
			t.Errorf("%s: second host with the same name: %v", table, err)
		}
	}
	for _, table := range []string{"host_metrics_raw", "host_metrics_1m", "host_metrics_1h"} {
		if _, err := database.Exec(`INSERT INTO ` + table + `
			(host_id, ts, cpu_percent, cpu_cores, mem_used, mem_total, disk_used, disk_total)
			VALUES ('local', 100, 5, 4, 1, 2, 3, 4)`); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
}

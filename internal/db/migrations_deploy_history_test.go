package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// Env-override deploys and rollbacks to them stored compose with .env
// values filled in; the upgrade clears those snapshots and keeps the rest.
func TestDeployHistoryEnvironmentMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261102111923_stack_targets.sql"); err != nil {
		t.Fatal(err)
	}
	const merged = "name: web\nservices:\n  db:\n    environment:\n      PASSWORD: hunter2\n"
	rows := []struct {
		stack, compose, note string
	}{
		{"web", "services: {db: {image: postgres}}\n", ""},
		{"web", merged, "env: prod"},
		{"web", merged, "rollback to #2"},
		{"web", "services: {db: {image: postgres:17}}\n", "rollback to #1"},
		{"other", merged, ""},
		{"edited", "services: {db: {image: postgres}}\n", ""},
		{"edited", "E: merged", "env: prod"},
		{"edited", "E: merged", "rollback to #2"},
		{"edited", "E: merged, then edited", ""},
		{"clean", "services: {a: {image: x}}\n", "env: prod"},
	}
	for _, r := range rows {
		if _, err := database.Exec(`INSERT INTO stack_deploy_history (stack_name, host_id, compose_yaml, note)
			VALUES (?, 'local', ?, NULLIF(?, ''))`, r.stack, r.compose, r.note); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	got := map[int64]string{}
	res, err := database.Query(`SELECT id, compose_yaml, environment FROM stack_deploy_history ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for res.Next() {
		var id int64
		var compose, env string
		if err := res.Scan(&id, &compose, &env); err != nil {
			t.Fatal(err)
		}
		if env != "" {
			t.Errorf("row %d: environment = %q, want empty", id, env)
		}
		got[id] = compose
	}
	want := map[int64]string{
		1: rows[0].compose,
		2: "",
		3: "",
		4: "",
		5: merged,
		6: rows[5].compose,
		7: "",
		8: "",
		9: "",
		10: "",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("row %d: compose = %q, want %q", id, got[id], w)
		}
	}

	var body string
	if err := database.QueryRow(`SELECT body FROM notices WHERE key = 'stacks.compose-env-values'`).Scan(&body); err != nil {
		t.Fatalf("notice: %v", err)
	}
	if !strings.Contains(body, "web") || !strings.Contains(body, "edited") || strings.Contains(body, "clean") || strings.Contains(body, "other") {
		t.Errorf("notice names the wrong stacks: %s", body)
	}
}

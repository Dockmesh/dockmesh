package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Upgrading must keep every API token working: tokens with a resolvable
// creator get it as owner, everything else is marked legacy.
func TestAPITokenOwnerMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// PRAGMA foreign_keys is per connection; keep one so toggling it below
	// applies to the inserts.
	database.SetMaxOpenConns(1)
	if err := migrateUpTo(database, "058_notices.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('6f1c0a52-1d1e-4a52-9a43-3b1b0f6f2d10', 'alice', 'x')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		prefix  string
		creator any
	}{
		{"dmt_owned001", "6f1c0a52-1d1e-4a52-9a43-3b1b0f6f2d10"},
		{"dmt_nocreatr", nil},
		{"dmt_goneuser", "11111111-2222-3333-4444-555555555555"},
	} {
		if _, err := database.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id)
			VALUES (?, 'h', ?, 'admin', ?)`, row.prefix, row.prefix, row.creator); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	want := map[string]struct {
		owner  sql.NullString
		legacy bool
	}{
		"dmt_owned001": {sql.NullString{String: "6f1c0a52-1d1e-4a52-9a43-3b1b0f6f2d10", Valid: true}, false},
		"dmt_nocreatr": {sql.NullString{}, true},
		"dmt_goneuser": {sql.NullString{}, true},
	}
	rows, err := database.Query(`SELECT token_prefix, created_by_user_id, legacy, typeof(created_by_user_id) FROM api_tokens`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var prefix, typ string
		var owner sql.NullString
		var legacy bool
		if err := rows.Scan(&prefix, &owner, &legacy, &typ); err != nil {
			t.Fatal(err)
		}
		seen++
		w := want[prefix]
		if owner != w.owner || legacy != w.legacy {
			t.Errorf("%s: owner=%v legacy=%v, want owner=%v legacy=%v", prefix, owner, legacy, w.owner, w.legacy)
		}
		if owner.Valid && typ != "text" {
			t.Errorf("%s: owner stored as %s", prefix, typ)
		}
	}
	if seen != 3 {
		t.Errorf("got %d tokens after migration, want 3", seen)
	}

	if _, err := database.Exec(`DELETE FROM users WHERE id = '6f1c0a52-1d1e-4a52-9a43-3b1b0f6f2d10'`); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullString
	if err := database.QueryRow(`SELECT created_by_user_id FROM api_tokens WHERE token_prefix = 'dmt_owned001'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Error("owner not cleared when the user was deleted")
	}
}

// Upgrading keeps existing alert rules as metric rules and tells admins
// about the new built-in event rules.
func TestEventAlertsMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "065_alert_engine.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'alice', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO alert_rules (name, metric, operator, threshold) VALUES ('mine', 'cpu_percent', 'gt', 80)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := database.QueryRow(`SELECT kind FROM alert_rules WHERE name = 'mine'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "metric" {
		t.Errorf("existing rule kind = %q, want metric", kind)
	}
	var notices int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = 'alerts.event-rules-added' AND audience = 'admins' AND resolved_at IS NULL`).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Errorf("upgrade notice count = %d, want 1", notices)
	}
}

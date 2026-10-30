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

// Enrollment tokens issued before expiry existed must stay usable for a
// grace window after the upgrade; enrolled agents get no expiry.
func TestAgentEnrollTokenExpiryMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "063_totp_last_step.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agents (id, name, enrollment_token_hash, status) VALUES
		('a1', 'pending-host', 'hash', 'pending'),
		('a2', 'enrolled-host', NULL, 'offline')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	var grace sql.NullFloat64
	if err := database.QueryRow(`SELECT (julianday(enrollment_token_expires_at) - julianday('now')) * 24
		FROM agents WHERE id = 'a1'`).Scan(&grace); err != nil {
		t.Fatal(err)
	}
	if !grace.Valid || grace.Float64 < 23 || grace.Float64 > 24.1 {
		t.Errorf("pending token grace = %v hours, want ~24", grace)
	}
	var enrolled sql.NullString
	if err := database.QueryRow(`SELECT enrollment_token_expires_at FROM agents WHERE id = 'a2'`).Scan(&enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled.Valid {
		t.Errorf("enrolled agent got an expiry: %v", enrolled.String)
	}
}

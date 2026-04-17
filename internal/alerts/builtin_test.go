package alerts

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newAlertsDB spins up the minimal schema needed for the Delete-protection
// test. We don't need the full alert_history machinery, just alert_rules
// with all the columns the scanner expects (matching migrations 010,
// 019, 024).
func newAlertsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	schema := `
	CREATE TABLE alert_rules (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		name              TEXT NOT NULL,
		container_filter  TEXT NOT NULL DEFAULT '*',
		metric            TEXT NOT NULL,
		operator          TEXT NOT NULL,
		threshold         REAL NOT NULL,
		duration_seconds  INTEGER NOT NULL DEFAULT 60,
		channel_ids       TEXT NOT NULL DEFAULT '[]',
		enabled           INTEGER NOT NULL DEFAULT 1,
		severity          TEXT NOT NULL DEFAULT 'warning',
		cooldown_seconds  INTEGER NOT NULL DEFAULT 300,
		muted_until       DATETIME,
		builtin           INTEGER NOT NULL DEFAULT 0,
		firing_since      DATETIME,
		last_triggered_at DATETIME,
		last_resolved_at  DATETIME,
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedBuiltin(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO alert_rules (name, metric, operator, threshold, builtin)
		VALUES ('Container CPU > 90%', 'cpu_percent', 'gt', 90, 1)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO alert_rules (name, metric, operator, threshold, builtin)
		VALUES ('My custom rule', 'cpu_percent', 'gt', 75, 0)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestDeleteBuiltinBlocked(t *testing.T) {
	db := newAlertsDB(t)
	svc := &Service{db: db}
	id := seedBuiltin(t, db)

	err := svc.Delete(context.Background(), id)
	if !errors.Is(err, ErrBuiltinImmutable) {
		t.Fatalf("expected ErrBuiltinImmutable, got %v", err)
	}

	// Row must still exist.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM alert_rules WHERE id = ?`, id).Scan(&n)
	if n != 1 {
		t.Errorf("builtin row was deleted despite error: count=%d", n)
	}
}

func TestDeleteUserRuleAllowed(t *testing.T) {
	db := newAlertsDB(t)
	svc := &Service{db: db}
	id := seedUser(t, db)

	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("expected success deleting user rule, got %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM alert_rules WHERE id = ?`, id).Scan(&n)
	if n != 0 {
		t.Errorf("user row survived delete: count=%d", n)
	}
}

func TestDeleteNonexistentIsNoop(t *testing.T) {
	db := newAlertsDB(t)
	svc := &Service{db: db}
	if err := svc.Delete(context.Background(), 99999); err != nil {
		t.Fatalf("deleting nonexistent should be no-op, got %v", err)
	}
}

package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A credential rewritten in place must not stay readable in the file:
// secure_delete covers rows rewritten from now on, Compact the leftovers
// of rows rewritten before it was on.
func TestRewrittenSecretsLeaveNoTrace(t *testing.T) {
	const secret = "plaintext-p4ssword-that-must-go"
	// Sealed values are longer than the plaintext, so the row moves to a
	// new cell and the old one is left behind in the page.
	sealed := `{"password":"enc:` + strings.Repeat("x", 300) + `"}`
	tests := []struct {
		name     string
		legacy   bool // rewrite through a connection without secure_delete
		compact  bool
		wantGone bool
	}{
		{"secure_delete zeroes the old row", false, false, true},
		{"legacy leftovers stay without compaction", true, false, false},
		{"compaction removes legacy leftovers", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			ctx := context.Background()
			write := func(d *sql.DB) {
				t.Helper()
				for _, q := range []string{
					`CREATE TABLE creds (id INTEGER PRIMARY KEY, cfg TEXT)`,
					`INSERT INTO creds (cfg) VALUES ('{"password":"` + secret + `"}')`,
					`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 20) INSERT INTO creds (cfg) SELECT 'other row ' || i FROM n`,
					`UPDATE creds SET cfg = '` + sealed + `' WHERE id = 1`,
					`PRAGMA wal_checkpoint(TRUNCATE)`,
				} {
					if _, err := d.ExecContext(ctx, q); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tt.legacy {
				legacy, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=secure_delete(0)")
				if err != nil {
					t.Fatal(err)
				}
				legacy.SetMaxOpenConns(1)
				write(legacy)
				legacy.Close()
			}
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if !tt.legacy {
				write(d)
			}
			if tt.compact {
				if err := Compact(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			var cfg string
			if err := d.QueryRowContext(ctx, `SELECT cfg FROM creds WHERE id = 1`).Scan(&cfg); err != nil || cfg != sealed {
				t.Fatalf("row = %q, %v", cfg, err)
			}
			found := false
			for _, p := range []string{path, path + "-wal"} {
				data, err := os.ReadFile(p)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				found = found || bytes.Contains(data, []byte(secret))
			}
			if found == tt.wantGone {
				t.Fatalf("secret gone from the database files = %v, want %v", !found, tt.wantGone)
			}
		})
	}
}

// A reader holding the WAL blocks the truncating checkpoint. SQLite still
// answers OK for that, so Compact must report it instead of claiming the
// file is clean.
func TestCompactReportsBlockedCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the busy timeout")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	conn, err := reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var v string
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, d); err == nil {
		t.Fatal("Compact succeeded while a reader held the WAL")
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, d); err != nil {
		t.Fatalf("Compact after the reader finished: %v", err)
	}
}

func TestCompactIfPendingClearsTheMarkOnlyOnSuccess(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	pending := func() bool {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key = ?`, compactPendingKey).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if err := CompactIfPending(ctx, d); err != nil || pending() {
		t.Fatalf("without a mark: err %v, pending %v", err, pending())
	}
	if err := MarkCompactPending(ctx, d); err != nil || !pending() {
		t.Fatalf("mark: err %v, pending %v", err, pending())
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := CompactIfPending(cancelled, d); err == nil || !pending() {
		t.Fatalf("failed compaction: err %v, pending %v", err, pending())
	}
	if err := CompactIfPending(ctx, d); err != nil || pending() {
		t.Fatalf("compaction: err %v, pending %v", err, pending())
	}
}

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
		legacy   bool // rewrite through a connection without secure_delete, as older releases did
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
				raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=secure_delete(0)")
				if err != nil {
					t.Fatal(err)
				}
				raw.SetMaxOpenConns(1)
				write(raw)
				raw.Close()
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
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if gone := !bytes.Contains(raw, []byte(secret)); gone != tt.wantGone {
				t.Fatalf("secret gone from the file = %v, want %v", gone, tt.wantGone)
			}
		})
	}
}

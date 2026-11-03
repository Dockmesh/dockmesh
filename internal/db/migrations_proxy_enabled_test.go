package db

import (
	"path/filepath"
	"testing"
)

// A fresh database has no saved proxy choice; an existing install keeps
// its stored 'false', which may have been saved in the UI.
func TestProxyEnabledUnsavedDefaultMigration(t *testing.T) {
	cases := []struct {
		name     string
		existing bool
		want     string
	}{
		{"fresh install", false, ""},
		{"existing install", true, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, err := Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := migrateUpTo(database, "20261102193312_notice_blocking.sql"); err != nil {
				t.Fatal(err)
			}
			if tc.existing {
				if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'admin', 'x')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(database); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := database.QueryRow(`SELECT value FROM settings WHERE key = 'proxy_enabled'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("proxy_enabled = %q, want %q", got, tc.want)
			}
		})
	}
}

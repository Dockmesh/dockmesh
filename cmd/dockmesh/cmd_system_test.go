package main

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

func TestReadUnitPaths(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string // nil = no settings table
		want     unitPathsOutput
	}{
		{"no settings table", nil, unitPathsOutput{}},
		{"roots never saved", map[string]string{"other": "x"}, unitPathsOutput{}},
		{"roots emptied by an admin", map[string]string{targets.LocalRootsSetting: ""}, unitPathsOutput{RootsSet: true}},
		{"roots, audit dir and empty-path dir", map[string]string{
			targets.LocalRootsSetting: "/srv/backups\n /mnt/nas ,/var/lib/dockmesh/data/backups\n",
			audit.RetentionLocalDir:   "/srv/audit",
			targets.EmptyPathSetting:  "/var/lib/dockmesh/data/backups",
		}, unitPathsOutput{RootsSet: true, Paths: []string{
			"/srv/audit", "/var/lib/dockmesh/data/backups", "/srv/backups", "/mnt/nas", "/var/lib/dockmesh/data/backups",
		}}},
		// Left for the caller to refuse: a relative entry stays relative
		// and a newline inside a value stays inside it.
		{"relative root and planted newline", map[string]string{
			targets.LocalRootsSetting: "backups",
			audit.RetentionLocalDir:   "/srv/a\nExecStartPre=/bin/sh",
		}, unitPathsOutput{RootsSet: true, Paths: []string{"/srv/a\nExecStartPre=/bin/sh", "backups"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dockmesh.db")
			db, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE other (x TEXT)`); err != nil {
				t.Fatal(err)
			}
			if tt.settings != nil {
				if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`); err != nil {
					t.Fatal(err)
				}
				for k, v := range tt.settings {
					if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
						t.Fatal(err)
					}
				}
			}
			db.Close()
			got, err := readUnitPaths(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

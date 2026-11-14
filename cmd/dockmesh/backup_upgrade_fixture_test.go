package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// fixtureTargets is what each job of testdata/v0.5.3-backup-jobs.sql
// writes to.
var fixtureTargets = map[string]struct {
	typ string
	cfg map[string]any
}{
	"sys-abs":     {"local", map[string]any{"path": "/srv/nas-backups"}},
	"sys-rel":     {"local", map[string]any{"path": "./rel-backups"}},
	"sys-default": {"local", map[string]any{}},
	"sys-smb": {"smb", map[string]any{"host": "nas.example.test", "share": "backups", "path": "dockmesh",
		"username": "dm", "password": "pw"}},
	"sys-s3": {"s3", map[string]any{"endpoint": "s3.example.test", "bucket": "dm-backups", "prefix": "srv1",
		"access_key": "AK", "secret_key": "SK"}},
}

func openV053Fixture(t *testing.T, dump []byte, dataDir string) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(dataDir, "dockmesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(string(dump)); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

// startBackups runs the backup steps of a server start that touch job
// targets, in the order main runs them.
func startBackups(t *testing.T, conn *sql.DB, dataDir string) (*backup.Service, *notices.Service) {
	t.Helper()
	ctx := context.Background()
	svc := backup.NewService(conn, nil, nil, nil, backup.SystemPaths{
		DBPath: filepath.Join(dataDir, "dockmesh.db"), StacksRoot: filepath.Join(dataDir, "stacks"), DataDir: dataDir,
	})
	n := notices.NewService(conn)
	svc.SetNotices(n)
	st := settings.NewStore(conn)
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.MigrateSystemJobPath(ctx); err != nil {
		t.Fatal(err)
	}
	if err := seedLocalBackupRoots(ctx, st, conn, svc, dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := pinEmptyLocalPath(ctx, st, dataDir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := prepareJobTargets(ctx, conn, st, svc, svc.Targets(), n, func(backup.Job, string) {}); err != nil {
		t.Fatal(err)
	}
	if err := syncMovedSystemTargetNotice(ctx, n, svc); err != nil {
		t.Fatal(err)
	}
	return svc, n
}

func readV053Fixture(t *testing.T) []byte {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("testdata", "v0.5.3-backup-jobs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

func TestUpgradeFromV053KeepsJobTargets(t *testing.T) {
	dump := readV053Fixture(t)
	cases := []struct {
		name string
		keep string // the only job left in the fixture; empty keeps all
	}{
		{"all jobs", ""},
		{"system job, absolute local path", "sys-abs"},
		{"system job, relative local path", "sys-rel"},
		{"system job, local target without a path", "sys-default"},
		{"system job, SMB target", "sys-smb"},
		{"system job, S3 target", "sys-s3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			t.Chdir(t.TempDir())
			dataDir := filepath.Join(t.TempDir(), "data")
			conn := openV053Fixture(t, dump, dataDir)
			if tc.keep != "" {
				if _, err := conn.Exec(`DELETE FROM backup_jobs WHERE name <> ?`, tc.keep); err != nil {
					t.Fatal(err)
				}
			}
			var targetsBefore int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM backup_targets`).Scan(&targetsBefore); err != nil {
				t.Fatal(err)
			}

			for start := 1; start <= 2; start++ {
				svc, n := startBackups(t, conn, dataDir)
				jobs, err := svc.ListJobs(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if tc.keep == "" && len(jobs) != len(fixtureTargets) {
					t.Fatalf("start %d: %d jobs, want %d", start, len(jobs), len(fixtureTargets))
				}
				for _, j := range jobs {
					want := fixtureTargets[j.Name]
					if j.TargetID != j.ID {
						t.Errorf("start %d: job %s now on target %d, want its own target %d", start, j.Name, j.TargetID, j.ID)
						continue
					}
					got, err := svc.Targets().Get(ctx, j.TargetID)
					if err != nil {
						t.Fatal(err)
					}
					if got.Type != want.typ || !reflect.DeepEqual(got.Config, want.cfg) {
						t.Errorf("start %d: job %s writes to %s %v, want %s %v", start, j.Name, got.Type, got.Config, want.typ, want.cfg)
					}
				}
				var targetsAfter int
				if err := conn.QueryRow(`SELECT COUNT(*) FROM backup_targets`).Scan(&targetsAfter); err != nil {
					t.Fatal(err)
				}
				if targetsAfter != targetsBefore {
					t.Errorf("start %d: %d saved targets, want %d", start, targetsAfter, targetsBefore)
				}
				open, err := n.Open(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, o := range open {
					if o.Key == "backups.system-job-path" || o.Key == "backups.system-target-moved" {
						t.Errorf("start %d: notice %s raised for an unchanged job", start, o.Key)
					}
				}
			}
		})
	}
}

// An install where an earlier version already moved the system job gets a
// notice naming the target the job was created with.
func TestMovedSystemTargetNotice(t *testing.T) {
	dump := readV053Fixture(t)
	ctx := context.Background()
	t.Chdir(t.TempDir())
	dataDir := filepath.Join(t.TempDir(), "data")
	conn := openV053Fixture(t, dump, dataDir)
	if _, err := conn.Exec(`DELETE FROM backup_jobs WHERE name <> 'sys-abs'`); err != nil {
		t.Fatal(err)
	}
	// What a start of v0.5.3 left behind: the job inline at <data>/backups,
	// moved to a new saved target on the same start.
	if _, err := conn.Exec(`UPDATE backup_jobs SET target_id = NULL, target_config = ? WHERE name = 'sys-abs'`,
		`{"path":"`+filepath.Join(dataDir, "backups")+`"}`); err != nil {
		t.Fatal(err)
	}
	// Saved after the job, so it can't be the target the job was created with.
	if _, err := conn.Exec(`INSERT INTO backup_targets (name, type, config_json) VALUES ('sys-abs (old)', 'local', '{"path":"/srv/elsewhere"}')`); err != nil {
		t.Fatal(err)
	}
	svc, n := startBackups(t, conn, dataDir)
	job, previous, err := svc.MovedSystemTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job != "sys-abs" || !reflect.DeepEqual(previous, []string{"/srv/nas-backups"}) {
		t.Fatalf("MovedSystemTarget = %q %v, want sys-abs [/srv/nas-backups]", job, previous)
	}
	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raised := false
	for _, o := range open {
		raised = raised || o.Key == "backups.system-target-moved"
	}
	if !raised {
		t.Fatal("no notice for the moved system job")
	}

	// Picking the old target again resolves it.
	if _, err := conn.Exec(`UPDATE backup_jobs SET target_id = 1 WHERE name = 'sys-abs'`); err != nil {
		t.Fatal(err)
	}
	if err := syncMovedSystemTargetNotice(ctx, n, svc); err != nil {
		t.Fatal(err)
	}
	open, err = n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range open {
		if o.Key == "backups.system-target-moved" {
			t.Fatal("notice still open after the job points at its old target again")
		}
	}
}

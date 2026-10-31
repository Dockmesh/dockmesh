package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestRepairJobSecretsRunsOnce(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	svc := backup.NewService(database, nil, nil, sec, backup.SystemPaths{})
	n := notices.NewService(database)
	svc.SetNotices(n)
	st := settings.NewStore(database)
	ts := targets.NewTargetStore(database)

	loc := func(pw string) map[string]any {
		return map[string]any{"host": "nas", "share": "bk", "username": "u", "password": pw}
	}
	if _, err := ts.Create(ctx, targets.TargetInput{Name: "nas", Type: "smb", Config: loc("s3cret")}); err != nil {
		t.Fatal(err)
	}
	// A job as 0.4.0 stored it: inline config, secret left blank.
	newJob := func(name string) {
		if _, err := database.Exec(`INSERT INTO backup_jobs (name, encrypt, target_type, target_config, sources)
			VALUES (?, 0, 'smb', '{"host":"nas","share":"bk","username":"u","password":""}', '[{"type":"volume","name":"pgdata"}]')`, name); err != nil {
			t.Fatal(err)
		}
	}
	repairs := 0
	count := func(backup.Job, string) { repairs++ }

	newJob("before")
	if err := repairJobSecretsOnce(ctx, st, svc, ts, n, count); err != nil {
		t.Fatal(err)
	}
	if repairs != 1 {
		t.Fatalf("first start repaired %d jobs, want 1", repairs)
	}

	newJob("after")
	if err := repairJobSecretsOnce(ctx, st, svc, ts, n, count); err != nil {
		t.Fatal(err)
	}
	if repairs != 1 {
		t.Errorf("later start repaired again (%d repairs)", repairs)
	}
}

func TestPrepareJobTargetsRepairsBeforeMigrating(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	svc := backup.NewService(database, nil, nil, nil, backup.SystemPaths{})
	n := notices.NewService(database)
	svc.SetNotices(n)
	st := settings.NewStore(database)
	ts := targets.NewTargetStore(database)

	saved, err := ts.Create(ctx, targets.TargetInput{Name: "nas", Type: "smb",
		Config: map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO backup_jobs (name, encrypt, target_type, target_config, sources)
		VALUES ('from-0.4.0', 0, 'smb', '{"host":"nas","share":"bk","username":"u","password":""}', '[{"type":"volume","name":"pgdata"}]')`); err != nil {
		t.Fatal(err)
	}
	repaired := 0
	if err := prepareJobTargets(ctx, st, svc, ts, n, func(backup.Job, string) { repaired++ }); err != nil {
		t.Fatal(err)
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	if repaired != 1 || jobs[0].TargetID != saved.ID {
		t.Fatalf("repaired %d, job target %d: the repaired job should reference the saved target %d", repaired, jobs[0].TargetID, saved.ID)
	}
	all, err := ts.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("%d saved targets, want only the original: %v", len(all), err)
	}
	open, _ := n.Open(ctx)
	for _, o := range open {
		if o.Key == "backups.job-secret-missing" {
			t.Fatalf("migrated job reported as missing its secret: %s", o.Body)
		}
	}
}

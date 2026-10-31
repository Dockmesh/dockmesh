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
	newJob := func(name string) *backup.Job {
		j, err := svc.CreateJob(ctx, backup.JobInput{
			Name: name, TargetType: "smb", TargetConfig: loc(""),
			Sources: []backup.Source{{Type: "volume", Name: "pgdata"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return j
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

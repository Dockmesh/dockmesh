//go:build integration

package handlers

import (
	"context"
	"maps"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/integration"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/redact"
)

// locationField is the setting that says which server a target talks to.
var locationField = map[string]string{"smb": "host", "sftp": "host", "webdav": "url", "s3": "endpoint"}

func TestJobsFromSavedTargetsAgainstRealServers(t *testing.T) {
	env := integration.LoadEnv(t)
	for _, c := range env.Targets(integration.RunID()) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Gaps[integration.GapAll] != "" {
				t.Skip(c.Gaps[integration.GapAll])
			}
			ctx := context.Background()
			svc, database := integration.NewService(t)
			store := targets.NewTargetStore(database)

			saved, err := store.Create(ctx, targets.TargetInput{Name: "nas", Type: c.Type, Config: c.Config})
			if err != nil {
				t.Fatal(err)
			}
			// A redacted target config, as a client may send it back.
			picked := func() backup.JobInput {
				return backup.JobInput{
					Name: "nightly", TargetType: c.Type, TargetConfig: redactTarget(saved).Config,
					Sources: []backup.Source{{Type: "system"}}, Enabled: true,
				}
			}
			if m := redact.ToMap(picked().TargetConfig); m[c.Secret] != "" || m[c.Secret+redact.SetSuffix] != true {
				t.Fatalf("target list leaks %s: %v", c.Secret, m)
			}

			t.Run("job from a saved target runs", func(t *testing.T) {
				in := picked()
				in.TargetID = saved.ID
				if err := resolveJobTargetSecrets(&in, nil); err != nil {
					t.Fatal(err)
				}
				job, err := svc.CreateJob(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				if job.TargetID != saved.ID {
					t.Fatalf("job references target %d, want %d", job.TargetID, saved.ID)
				}
				run, err := svc.RunNow(ctx, job.ID)
				if err != nil || run.Status != "success" {
					t.Fatalf("run: %v %+v", err, run)
				}

				// Editing the job keeps the reference; the secret stays on the target.
				edit := backup.JobInput{
					Name: job.Name, TargetID: job.TargetID, TargetType: job.TargetType, TargetConfig: redactJob(job).TargetConfig,
					Sources: job.Sources, Enabled: true, RetentionCount: 3,
				}
				if err := resolveJobTargetSecrets(&edit, job); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.UpdateJob(ctx, job.ID, edit); err != nil {
					t.Fatal(err)
				}
				run, err = svc.RunNow(ctx, job.ID)
				if err != nil || run.Status != "success" {
					t.Fatalf("run after edit: %v %+v", err, run)
				}
			})

			t.Run("redacted copy without a target id is refused", func(t *testing.T) {
				in := picked()
				if err := resolveJobTargetSecrets(&in, nil); err == nil {
					t.Fatal("job with a blank secret was accepted")
				}
			})

			t.Run("config sent with a target id is ignored", func(t *testing.T) {
				in := picked()
				in.Name = "nightly-other"
				in.TargetID = saved.ID
				cfg := maps.Clone(redact.ToMap(in.TargetConfig))
				cfg[locationField[c.Type]] = "other.example.org"
				in.TargetConfig = cfg
				job, err := svc.CreateJob(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				if m := redact.ToMap(job.TargetConfig); len(m) != 0 {
					t.Fatalf("job kept an inline config next to its target: %v", m)
				}
				run, err := svc.RunNow(ctx, job.ID)
				if err != nil || run.Status != "success" {
					t.Fatalf("run against the saved target: %v %+v", err, run)
				}
			})
		})
	}
}

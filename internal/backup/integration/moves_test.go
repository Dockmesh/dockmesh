//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// TestRunsAfterTargetMoves edits where a saved target writes (path, or
// the prefix for S3) between runs: older runs read from where they were
// written, and retention deletes their archives there.
func TestRunsAfterTargetMoves(t *testing.T) {
	env := LoadEnv(t)
	base := RunID() + "-mv"
	for _, c := range env.Targets(base) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Gaps[GapAll] != "" {
				t.Skip(c.Gaps[GapAll])
			}
			ctx := context.Background()
			svc, _ := NewService(t)
			ts := svc.Targets()
			stored, err := ts.Create(ctx, targets.TargetInput{Name: "t-" + c.Name, Type: c.Type, Config: c.Config})
			if err != nil {
				t.Fatal(err)
			}
			in := systemJob("moving", c)
			in.TargetID, in.TargetType, in.TargetConfig = stored.ID, "", nil
			in.RetentionCount = 2
			job, err := svc.CreateJob(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			run := func() *backup.Run {
				t.Helper()
				r, err := svc.RunNow(ctx, job.ID)
				if err != nil || r.Status != backup.StatusSuccess {
					t.Fatalf("run: %v %+v", err, r)
				}
				return r
			}
			before := run()

			moved := maps.Clone(c.Config)
			field := "path"
			if c.Type == "s3" {
				field = "prefix"
			}
			moved[field] = moved[field].(string) + "-moved"
			n, err := svc.PrepareTargetChange(ctx, stored.ID, c.Type, moved)
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("runs at the previous location = %d, want 1", n)
			}
			if _, err := ts.Update(ctx, stored.ID, targets.TargetInput{Name: stored.Name, Type: c.Type, Config: moved}); err != nil {
				t.Fatal(err)
			}
			after := run()

			for _, r := range []*backup.Run{before, after} {
				rc, err := svc.ReadRun(ctx, r.ID)
				if err != nil {
					t.Fatalf("read run %d: %v", r.ID, err)
				}
				if _, err := io.Copy(io.Discard, rc); err != nil {
					t.Fatalf("read run %d: %v", r.ID, err)
				}
				rc.Close()
			}
			if len(readFile(t, build(t, c, c.Config), before.TargetPath)) == 0 {
				t.Fatal("first archive is not at the old location")
			}

			run()
			got, err := svc.GetRun(ctx, before.ID)
			if err != nil || got.PrunedAt == nil {
				t.Fatalf("oldest run not pruned: %v %+v", err, got)
			}
			_, err = build(t, c, c.Config).Read(ctx, before.TargetPath)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("retention left the archive at the old location: %v", err)
			}
		})
	}
}

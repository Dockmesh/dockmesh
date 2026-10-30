//go:build integration

package integration

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup"
)

func systemJob(name string, c Config) backup.JobInput {
	return backup.JobInput{
		Name: name, TargetType: c.Type, TargetConfig: c.Config,
		Sources: []backup.Source{{Type: "system"}}, Enabled: true,
	}
}

func TestJobs(t *testing.T) {
	env := LoadEnv(t)
	base := RunID()
	for _, c := range env.Targets(base) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Gaps[GapAll] != "" {
				t.Skip(c.Gaps[GapAll])
			}
			ctx := context.Background()
			svc, _ := NewService(t)
			tg := build(t, c, c.Config)

			job, err := svc.CreateJob(ctx, systemJob("nightly", c))
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.RunNow(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "success" {
				t.Fatalf("run %s: %s", run.Status, run.Error)
			}

			// The job folder does not exist before the first run.
			if !strings.HasPrefix(run.TargetPath, "jobs/nightly/") {
				t.Fatalf("archive path %q is not below jobs/nightly/", run.TargetPath)
			}
			if len(readFile(t, tg, run.TargetPath)) == 0 {
				t.Fatal("archive is empty")
			}
			if c.Gaps[GapList] == "" {
				entries, err := tg.List(ctx, "jobs/nightly/")
				if err != nil || len(entries) != 1 || entries[0].Path != run.TargetPath {
					t.Fatalf("target holds %+v (%v), want only %s", entries, err, run.TargetPath)
				}
			}

			t.Run("archive reads back", func(t *testing.T) {
				rc, err := svc.ReadRun(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer rc.Close()
				h := sha256.New()
				gz, err := gzip.NewReader(io.TeeReader(rc, h))
				if err != nil {
					t.Fatal(err)
				}
				tr := tar.NewReader(gz)
				var sawDB bool
				for {
					hdr, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("tar: %v", err)
					}
					if hdr.Name == "dockmesh.db" {
						sawDB = true
					}
				}
				_, _ = io.Copy(io.Discard, rc)
				if !sawDB {
					t.Fatal("archive has no dockmesh.db")
				}
				if got := hex.EncodeToString(h.Sum(nil)); got != run.SHA256 {
					t.Fatalf("stored bytes hash to %s, run recorded %s", got, run.SHA256)
				}
			})

			t.Run("edit keeps working", func(t *testing.T) {
				in := systemJob("nightly", c)
				in.RetentionCount = 2
				if _, err := svc.UpdateJob(ctx, job.ID, in); err != nil {
					t.Fatal(err)
				}
				r, err := svc.RunNow(ctx, job.ID)
				if err != nil || r.Status != "success" {
					t.Fatalf("run after edit: %v %+v", err, r)
				}
			})

			t.Run("retention keeps exactly N", func(t *testing.T) {
				c.SkipGap(t, GapList)
				for range 3 {
					time.Sleep(1100 * time.Millisecond)
					r, err := svc.RunNow(ctx, job.ID)
					if err != nil || r.Status != "success" {
						t.Fatalf("run: %v %+v", err, r)
					}
				}
				entries, err := tg.List(ctx, "jobs/nightly/")
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 2 {
					t.Fatalf("%d archives kept, want 2: %+v", len(entries), entries)
				}
			})

			t.Run("wrong credentials fail the run", func(t *testing.T) {
				bad := Config{Name: c.Name, Type: c.Type, Secret: c.Secret, Config: maps.Clone(c.Config)}
				bad.Config[c.Secret] = wrongSecret(t, c.Secret)
				j, err := svc.CreateJob(ctx, systemJob("denied", bad))
				if err != nil {
					t.Fatal(err)
				}
				r, err := svc.RunNow(ctx, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if r.Status != "failed" || r.Error == "" {
					t.Fatalf("run = %s, error %q; want a failure with a message", r.Status, r.Error)
				}
			})
		})
	}
}

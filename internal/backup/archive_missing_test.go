package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countLog counts the lines of run's log that contain s.
func countLog(t *testing.T, svc *Service, runID int64, s string) int {
	t.Helper()
	entries, err := svc.RunLog(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.Contains(e.Line, s) {
			n++
		}
	}
	return n
}

// Runs an older version's retention deleted without marking them come
// over as successful runs without a file. Retention lists them as archive
// missing once instead of warning on every pass, and a restore says why it
// can't read them.
func TestRetentionListsRunsPrunedByAnOlderVersion(t *testing.T) {
	ctx := context.Background()
	lt := newLocTest(t, 0)
	var old []*Run
	for i := 0; i < 3; i++ {
		r := lt.run(t)
		legacy(t, lt.svc, r.ID)
		old = append(old, r)
	}
	if _, err := lt.svc.store.db.Exec(`UPDATE backup_jobs SET retention_count = 1 WHERE id = ?`, lt.job.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range old[:2] {
		if err := os.Remove(filepath.Join(lt.rootA, filepath.FromSlash(r.TargetPath))); err != nil {
			t.Fatal(err)
		}
	}
	first := lt.run(t)
	for _, r := range old[:2] {
		got, err := lt.svc.GetRun(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.PrunedAt != nil || got.ArchiveMissingAt == nil || !strings.Contains(got.ArchiveMissingReason, r.TargetPath) {
			t.Fatalf("run %d: pruned %v, missing %v %q", r.ID, got.PrunedAt, got.ArchiveMissingAt, got.ArchiveMissingReason)
		}
		_, err = lt.svc.Restore(ctx, r.ID, RestoreOptions{Target: "copy"})
		if !errors.Is(err, ErrArchiveGone) {
			t.Fatalf("restore of run %d: %v, want ErrArchiveGone", r.ID, err)
		}
	}
	if got, _ := lt.svc.GetRun(ctx, old[2].ID); got.PrunedAt == nil || got.ArchiveMissingAt != nil {
		t.Fatalf("run with its archive: pruned %v, missing %v", got.PrunedAt, got.ArchiveMissingAt)
	}
	if n := countLog(t, lt.svc, first.ID, "listed as archive missing"); n != 2 {
		t.Fatalf("first pass logged %d archive-missing lines, want 2", n)
	}
	second := lt.run(t)
	if n := countLog(t, lt.svc, second.ID, "retention: archive"); n != 0 {
		t.Fatalf("the second retention pass logged %d lines about the missing runs", n)
	}
}

// A read finds a deleted archive gone and marks the run; once the file is
// back the next read unmarks it. A target that can't be read is not
// taken for a missing archive.
func TestArchiveMissingFollowsReads(t *testing.T) {
	ctx := context.Background()
	lt := newLocTest(t, 0)
	r := lt.run(t)
	path := filepath.Join(lt.rootA, filepath.FromSlash(r.TargetPath))
	aside := filepath.Join(t.TempDir(), "aside")
	if err := os.Rename(path, aside); err != nil {
		t.Fatal(err)
	}
	if _, err := lt.svc.VerifyRun(ctx, r.ID); !errors.Is(err, ErrArchiveGone) {
		t.Fatalf("verify without the file: %v", err)
	}
	got, err := lt.svc.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchiveMissingAt == nil || got.ArchiveMissingReason == "" {
		t.Fatal("run not marked archive missing")
	}
	if err := os.Rename(aside, path); err != nil {
		t.Fatal(err)
	}
	if err := lt.readable(t, r); err != nil {
		t.Fatalf("read with the file back: %v", err)
	}
	if got, _ := lt.svc.GetRun(ctx, r.ID); got.ArchiveMissingAt != nil || got.ArchiveMissingReason != "" {
		t.Fatalf("run still marked after its archive was read: %v %q", got.ArchiveMissingAt, got.ArchiveMissingReason)
	}

	// The target directory replaced by a file: reading fails, but not
	// because the archive is gone.
	moved := filepath.Join(t.TempDir(), "root")
	if err := os.Rename(lt.rootA, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lt.rootA); _ = os.Rename(moved, lt.rootA) })
	if err := os.WriteFile(lt.rootA, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lt.svc.VerifyRun(ctx, r.ID)
	if err == nil || errors.Is(err, ErrArchiveGone) {
		t.Fatalf("verify of an unreadable target: %v", err)
	}
	if got, _ := lt.svc.GetRun(ctx, r.ID); got.ArchiveMissingAt != nil {
		t.Fatal("an unreadable target marked the run archive missing")
	}
}

func TestRemoveMissingRun(t *testing.T) {
	ctx := context.Background()
	lt := newLocTest(t, 0)
	kept := lt.run(t)
	gone := lt.run(t)
	if err := lt.svc.RemoveMissingRun(ctx, kept.ID); !errors.Is(err, ErrArchivePresent) {
		t.Fatalf("remove a run with its archive: %v", err)
	}
	if err := os.Remove(filepath.Join(lt.rootA, filepath.FromSlash(gone.TargetPath))); err != nil {
		t.Fatal(err)
	}
	if _, err := lt.svc.VerifyRun(ctx, gone.ID); !errors.Is(err, ErrArchiveGone) {
		t.Fatalf("verify: %v", err)
	}
	if err := lt.svc.RemoveMissingRun(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lt.svc.GetRun(ctx, gone.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("removed run: %v", err)
	}
	var logs int
	if err := lt.svc.store.db.QueryRow(`SELECT COUNT(*) FROM backup_run_logs WHERE run_id = ?`, gone.ID).Scan(&logs); err != nil || logs != 0 {
		t.Fatalf("log rows of the removed run: %d %v", logs, err)
	}
	if _, err := lt.svc.GetRun(ctx, kept.ID); err != nil {
		t.Fatalf("the other run: %v", err)
	}
}

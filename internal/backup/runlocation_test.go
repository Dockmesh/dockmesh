package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// locTest is a job on a saved local target below one of two roots.
type locTest struct {
	svc        *Service
	host       *importHost
	rootA      string
	rootB      string
	target     *targets.StoredTarget
	job        *Job
	retainRuns int
}

func newLocTest(t *testing.T, retain int) *locTest {
	t.Helper()
	svc, fake, rootA := newExecTest(t, false)
	rootB := t.TempDir()
	targets.SetLocalRoots(func() []string { return []string{rootA, rootB} })
	host := newImportHost(fake)
	svc.SetHostResolver(host)
	ctx := context.Background()
	tgt, err := svc.Targets().Create(ctx, targets.TargetInput{Name: "nas", Type: "local", Config: map[string]any{"path": rootA}})
	if err != nil {
		t.Fatal(err)
	}
	in := volumeJob("db", "")
	in.TargetID, in.TargetType, in.TargetConfig = tgt.ID, "", nil
	in.RetentionCount = retain
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return &locTest{svc: svc, host: host, rootA: rootA, rootB: rootB, target: tgt, job: job, retainRuns: retain}
}

func (lt *locTest) run(t *testing.T) *Run {
	t.Helper()
	r, err := lt.svc.RunNow(context.Background(), lt.job.ID)
	if err != nil || r.Status != StatusSuccess {
		t.Fatalf("run: %v %+v", err, r)
	}
	return r
}

// movePath saves the target with a new path the way the API does.
func (lt *locTest) movePath(t *testing.T, path string) int {
	t.Helper()
	ctx := context.Background()
	cfg := map[string]any{"path": path}
	n, err := lt.svc.PrepareTargetChange(ctx, lt.target.ID, "local", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.svc.Targets().Update(ctx, lt.target.ID, targets.TargetInput{Name: "nas", Type: "local", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	return n
}

func (lt *locTest) readable(t *testing.T, r *Run) error {
	t.Helper()
	ctx := context.Background()
	if _, err := lt.svc.VerifyRun(ctx, r.ID); err != nil {
		return err
	}
	_, err := lt.svc.Restore(ctx, r.ID, RestoreOptions{Target: "copy"})
	return err
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func TestRunsAfterTargetPathEdit(t *testing.T) {
	lt := newLocTest(t, 5)
	var runs []*Run
	for i := 0; i < 3; i++ {
		runs = append(runs, lt.run(t))
	}
	if runs[0].TargetID != lt.target.ID {
		t.Fatalf("run target_id = %d, want %d", runs[0].TargetID, lt.target.ID)
	}
	if n := lt.movePath(t, lt.rootB); n != 3 {
		t.Fatalf("runs at the previous location = %d, want 3", n)
	}
	for i := 0; i < 2; i++ {
		runs = append(runs, lt.run(t))
	}
	for i, r := range runs {
		root := lt.rootA
		if i >= 3 {
			root = lt.rootB
		}
		if !exists(root, r.TargetPath) {
			t.Fatalf("run %d: archive %s not under %s: A=%v B=%v", i, r.TargetPath, root, archives(t, lt.rootA), archives(t, lt.rootB))
		}
		if err := lt.readable(t, r); err != nil {
			t.Fatalf("run %d after the path edit: %v", i, err)
		}
	}

	// Retention keeps 5: the oldest goes, from where it was written.
	lt.run(t)
	if exists(lt.rootA, runs[0].TargetPath) {
		t.Fatal("retention left the oldest archive at the old path")
	}
	got, _ := lt.svc.GetRun(context.Background(), runs[0].ID)
	if got.PrunedAt == nil {
		t.Fatal("oldest run not pruned")
	}
	if !exists(lt.rootA, runs[1].TargetPath) {
		t.Fatal("retention removed an archive it keeps")
	}
}

func TestRunsAfterJobSwitch(t *testing.T) {
	lt := newLocTest(t, 0)
	ctx := context.Background()
	old := lt.run(t)
	other, err := lt.svc.Targets().Create(ctx, targets.TargetInput{Name: "spare", Type: "local", Config: map[string]any{"path": lt.rootB}})
	if err != nil {
		t.Fatal(err)
	}
	in := volumeJob("db", "")
	in.TargetID, in.TargetType, in.TargetConfig = other.ID, "", nil
	job, err := lt.svc.UpdateJob(ctx, lt.job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if job.RunsAtPreviousLocation != 1 {
		t.Fatalf("runs at the previous location = %d, want 1", job.RunsAtPreviousLocation)
	}
	fresh := lt.run(t)
	if !exists(lt.rootB, fresh.TargetPath) || !exists(lt.rootA, old.TargetPath) {
		t.Fatal("archives not where the runs wrote them")
	}
	for _, r := range []*Run{old, fresh} {
		if err := lt.readable(t, r); err != nil {
			t.Fatalf("run %d: %v", r.ID, err)
		}
	}

	// The old target still holds the archive: it can't be deleted.
	if err := lt.svc.Targets().Delete(ctx, lt.target.ID); !errors.Is(err, targets.ErrTargetInUse) {
		t.Fatalf("delete of a target holding archives: %v", err)
	}
	if _, err := lt.svc.store.db.Exec(`DELETE FROM backup_targets WHERE id = ?`, lt.target.ID); err != nil {
		t.Fatal(err)
	}
	err = lt.readable(t, old)
	if !errors.Is(err, ErrArchiveMissing) || !strings.Contains(err.Error(), "was deleted") || !strings.Contains(err.Error(), lt.rootA) {
		t.Fatalf("run on a deleted target: %v", err)
	}
}

// legacy clears what a run recorded, like a run written before this
// version.
func legacy(t *testing.T, svc *Service, runID int64) {
	t.Helper()
	if _, err := svc.store.db.Exec(`UPDATE backup_runs SET target_id = NULL, target_location = NULL WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyRuns(t *testing.T) {
	cases := []struct {
		name    string
		change  func(t *testing.T, lt *locTest)
		wantErr string // empty: still readable
	}{
		{"target unchanged", func(*testing.T, *locTest) {}, ""},
		{"target edited after the upgrade", func(t *testing.T, lt *locTest) { lt.movePath(t, lt.rootB) }, ""},
		{"job switched after the upgrade", func(t *testing.T, lt *locTest) {
			other, err := lt.svc.Targets().Create(context.Background(), targets.TargetInput{Name: "spare", Type: "local", Config: map[string]any{"path": lt.rootB}})
			if err != nil {
				t.Fatal(err)
			}
			in := volumeJob("db", "")
			in.TargetID, in.TargetType, in.TargetConfig = other.ID, "", nil
			if _, err := lt.svc.UpdateJob(context.Background(), lt.job.ID, in); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"target edited before the upgrade", func(t *testing.T, lt *locTest) {
			if _, err := lt.svc.store.db.Exec(`UPDATE backup_targets SET config_json = ?, updated_at = datetime('now', '+1 minute') WHERE id = ?`,
				`{"path":"`+lt.rootB+`"}`, lt.target.ID); err != nil {
				t.Fatal(err)
			}
		}, "most likely still where that target pointed before the change"},
		{"archive deleted outside", func(t *testing.T, lt *locTest) {}, "not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lt := newLocTest(t, 0)
			r := lt.run(t)
			legacy(t, lt.svc, r.ID)
			if tc.name == "archive deleted outside" {
				if err := os.Remove(filepath.Join(lt.rootA, filepath.FromSlash(r.TargetPath))); err != nil {
					t.Fatal(err)
				}
			}
			tc.change(t, lt)
			err := lt.readable(t, r)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrArchiveMissing) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an archive-missing error with %q", err, tc.wantErr)
			}
		})
	}
}

func TestInferredLocationNotFound(t *testing.T) {
	lt := newLocTest(t, 0)
	r := lt.run(t)
	legacy(t, lt.svc, r.ID)
	// The archive really sat at an even older place: the stamp is wrong.
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(lt.rootA, "jobs"), moved); err != nil {
		t.Fatal(err)
	}
	lt.movePath(t, lt.rootB)
	err := lt.readable(t, r)
	if !errors.Is(err, ErrArchiveMissing) || !strings.Contains(err.Error(), "before the target changed on") ||
		!strings.Contains(err.Error(), lt.rootA) || !strings.Contains(err.Error(), lt.rootB) {
		t.Fatalf("got %v", err)
	}
}

func TestRetentionOfLegacyRunsAfterEdit(t *testing.T) {
	lt := newLocTest(t, 1)
	old := lt.run(t)
	legacy(t, lt.svc, old.ID)
	lt.movePath(t, lt.rootB)
	lt.run(t)
	if exists(lt.rootA, old.TargetPath) {
		t.Fatal("retention left a pre-upgrade archive at the target's earlier path")
	}
}

func TestEditWithoutMoveStampsNothing(t *testing.T) {
	lt := newLocTest(t, 0)
	r := lt.run(t)
	legacy(t, lt.svc, r.ID)
	if n := lt.movePath(t, lt.rootA); n != 0 {
		t.Fatalf("an edit that keeps the path reported %d runs elsewhere", n)
	}
	var loc *string
	if err := lt.svc.store.db.QueryRow(`SELECT target_location FROM backup_runs WHERE id = ?`, r.ID).Scan(&loc); err != nil {
		t.Fatal(err)
	}
	if loc != nil {
		t.Fatalf("a legacy run got a location without a move: %s", *loc)
	}
}

// A run recorded on a server the target no longer points at is never read
// with the target's current credentials: whoever edited the target in
// between could have chosen that server.
func TestRecordedServerNeedsItsOwnCredentials(t *testing.T) {
	svc, _ := newTestService(t, false)
	ctx := context.Background()
	tgt, err := svc.Targets().Create(ctx, targets.TargetInput{Name: "nas", Type: "sftp",
		Config: map[string]any{"host": "nas.lan", "username": "backup", "password": "real-secret", "path": "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	in := volumeJob("db", "")
	in.TargetID, in.TargetType, in.TargetConfig = tgt.ID, "", nil
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	loc := `{"type":"sftp","config":{"host":"evil.example","username":"backup","path":"/b","host_key":"ssh-ed25519 AAAA"}}`
	if _, err := svc.store.db.Exec(`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sources_json, target_id, target_location)
		VALUES (9, ?, 'db', 'local', 'success', 'jobs/1-db/r9.tar.gz', '[{"type":"volume","name":"data"}]', ?, ?)`, job.ID, tgt.ID, loc); err != nil {
		t.Fatal(err)
	}
	dialed := false
	wrapRunTarget = func(tg targets.Target) targets.Target { dialed = true; return tg }
	t.Cleanup(func() { wrapRunTarget = func(tg targets.Target) targets.Target { return tg } })
	_, err = svc.ReadRun(ctx, 9)
	if !errors.Is(err, ErrArchiveMissing) || !strings.Contains(err.Error(), "another server or account") || dialed {
		t.Fatalf("got %v (target built: %v)", err, dialed)
	}
}

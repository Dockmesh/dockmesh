package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// Older versions deleted jobs outright and their runs recorded no target:
// such runs read their archive from whichever saved target holds the file
// with the run's hash, and say what to do when none does.
func TestRunWithoutJobRow(t *testing.T) {
	cases := []struct {
		name    string
		encrypt bool
		breakIt func(t *testing.T, svc *Service, root, other string, run *Run)
		wantErr error
	}{
		{name: "plain archive on a saved target"},
		{name: "encrypted archive on a saved target", encrypt: true},
		{
			name: "archive on another saved target",
			breakIt: func(t *testing.T, svc *Service, root, other string, run *Run) {
				moveFile(t, filepath.Join(root, filepath.FromSlash(run.TargetPath)), filepath.Join(other, filepath.FromSlash(run.TargetPath)))
			},
		},
		{
			name: "target deleted too",
			breakIt: func(t *testing.T, svc *Service, root, other string, run *Run) {
				mustExec(t, svc, `DELETE FROM backup_targets WHERE config_json LIKE ?`, "%"+root+"%")
			},
			wantErr: ErrRunJobGone,
		},
		{
			name: "no hash recorded",
			breakIt: func(t *testing.T, svc *Service, root, other string, run *Run) {
				mustExec(t, svc, `UPDATE backup_runs SET sha256 = '' WHERE id = ?`, run.ID)
			},
			wantErr: ErrRunJobGone,
		},
		{
			name: "file at the path is another archive",
			breakIt: func(t *testing.T, svc *Service, root, other string, run *Run) {
				p := filepath.Join(root, filepath.FromSlash(run.TargetPath))
				if err := os.WriteFile(p, []byte("not this run's archive"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrArchiveMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, fake, root := newExecTest(t, tc.encrypt)
			other := t.TempDir()
			targets.SetLocalRoots(func() []string { return []string{root, other} })
			svc.SetHostResolver(newImportHost(fake))
			var tid int64
			for _, dir := range []string{other, root} {
				tgt, err := svc.Targets().Create(ctx, targets.TargetInput{Name: filepath.Base(dir), Type: "local", Config: map[string]any{"path": dir}})
				if err != nil {
					t.Fatal(err)
				}
				tid = tgt.ID
			}
			in := volumeJob("db", "")
			in.TargetID, in.TargetType, in.TargetConfig = tid, "", nil
			in.Encrypt = tc.encrypt
			job, err := svc.CreateJob(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.RunNow(ctx, job.ID)
			if err != nil || run.Status != StatusSuccess {
				t.Fatalf("run: %v %+v", err, run)
			}
			if run.Encrypted != tc.encrypt {
				t.Fatalf("run encrypted = %v, want %v", run.Encrypted, tc.encrypt)
			}
			mustExec(t, svc, `UPDATE backup_runs SET target_id = NULL, target_location = NULL WHERE id = ?`, run.ID)
			mustExec(t, svc, `DELETE FROM backup_jobs WHERE id = ?`, job.ID)
			if tc.breakIt != nil {
				tc.breakIt(t, svc, root, other, run)
			}

			if _, err := svc.RunHost(ctx, run.ID); err != nil {
				t.Fatalf("run host: %v", err)
			}
			res, verr := svc.VerifyRun(ctx, run.ID)
			_, rerr := svc.Restore(ctx, run.ID, RestoreOptions{Target: "copy"})
			derr := readAll(svc, run.ID)
			if tc.wantErr == nil {
				if verr != nil || !res.Passed {
					t.Fatalf("verify: %v %+v", verr, res)
				}
				if rerr != nil {
					t.Fatalf("restore: %v", rerr)
				}
				if derr != nil {
					t.Fatalf("download: %v", derr)
				}
				return
			}
			if !errors.Is(rerr, tc.wantErr) {
				t.Errorf("restore error = %v, want %v", rerr, tc.wantErr)
			}
			if !errors.Is(derr, tc.wantErr) {
				t.Errorf("download error = %v, want %v", derr, tc.wantErr)
			}
			if verr == nil && res.Passed {
				t.Errorf("verify passed: %+v", res)
			}
		})
	}
}

func readAll(svc *Service, runID int64) error {
	rc, err := svc.ReadRun(context.Background(), runID)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

func mustExec(t *testing.T, svc *Service, q string, args ...any) {
	t.Helper()
	if _, err := svc.store.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func moveFile(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

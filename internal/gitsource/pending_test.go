package gitsource

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// A commit is only kept pending when the operation holding the stack will
// not deploy it: a deploy that started after the files were copied already
// deploys them, or a rollback the user chose instead.
func TestSyncPendingOnlyWhenHolderMissesCommit(t *testing.T) {
	tests := []struct {
		name        string
		op          string
		sinceOffset time.Duration // holder start relative to the deploy call
		wantPending bool
	}{
		{"image update", stacklock.OpImageUpdate, time.Millisecond, true},
		{"deploy started before the copy", stacklock.OpDeploy, -time.Hour, true},
		{"deploy started after the copy", stacklock.OpDeploy, 0, false},
		{"dependency deploy after the copy", stacklock.OpDependency, 0, false},
		{"scale after the copy", stacklock.OpScale, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			repoDir := t.TempDir()
			repo, err := git.PlainInitWithOptions(repoDir, &git.PlainInitOptions{
				InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
			})
			if err != nil {
				t.Fatal(err)
			}
			commitCompose(t, repo, repoDir, "1")
			busy := false
			deploy := func(context.Context, string) (any, error) {
				if !busy {
					return nil, nil
				}
				return nil, &stacklock.BusyError{Stack: "web", Operation: tt.op, Since: time.Now().Add(tt.sinceOffset)}
			}
			svc := New(database, nil, mgr, t.TempDir(), deploy)
			// Without it the background retry after a busy deploy would
			// drop the pending commit before the check below.
			svc.SetDeployNow(deploy)
			defer svc.Stop()
			if _, err := svc.Configure(ctx, "web", Input{RepoURL: repoDir, AutoDeploy: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Sync(ctx, "web"); err != nil {
				t.Fatal(err)
			}
			busy = true
			commitCompose(t, repo, repoDir, "2")
			if _, err := svc.Sync(ctx, "web"); err != nil {
				t.Fatal(err)
			}
			src, err := svc.Get(ctx, "web")
			if err != nil {
				t.Fatal(err)
			}
			if got := src.PendingDeploy != nil; got != tt.wantPending {
				t.Fatalf("pending = %+v, want pending %v", src.PendingDeploy, tt.wantPending)
			}
		})
	}
}

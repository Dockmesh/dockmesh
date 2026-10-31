package gitsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func commitCompose(t *testing.T, repo *git.Repository, dir, rev string) {
	t.Helper()
	compose := "services:\n  web:\n    image: nginx\n    environment:\n      REV: \"" + rev + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("compose.yaml"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	if _, err := wt.Commit("rev "+rev, &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
}

// A sync must not take over stack files changed outside Dockmesh: the copy
// would record them as written by Dockmesh and the deploy check would pass.
func TestSyncHoldsCommitWhenStackFilesChangedOutside(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	mgr.SetSumStore(stacks.NewSumStore(database))
	n := notices.NewService(database)
	mgr.SetNotices(n)

	repoDir := t.TempDir()
	repo, err := git.PlainInitWithOptions(repoDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitCompose(t, repo, repoDir, "1")

	deploys := 0
	svc := New(database, nil, mgr, t.TempDir(), func(ctx context.Context, stack string) (any, error) {
		if err := mgr.CheckGitDeploy(ctx, stack); err != nil {
			return nil, err
		}
		deploys++
		return nil, nil
	})
	if _, err := svc.Configure(ctx, "web", Input{RepoURL: repoDir, AutoDeploy: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(ctx, "web"); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	stackDir, err := mgr.Dir("web")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDir, ".env"), []byte("INJECTED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitCompose(t, repo, repoDir, "2")
	before := deploys

	for i := 0; i < 2; i++ {
		if _, err := svc.Sync(ctx, "web"); !errors.Is(err, stacks.ErrModifiedOutside) {
			t.Fatalf("sync %d with outside change: err = %v, want ErrModifiedOutside", i, err)
		}
	}
	if deploys != before {
		t.Fatalf("held commit was deployed")
	}
	detail, err := mgr.Get("web")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail.Compose, `REV: "2"`) {
		t.Fatal("held commit was copied into the stack")
	}
	if changed, _ := mgr.ModifiedOutside("web"); len(changed) == 0 {
		t.Fatal("outside change no longer detected after the sync")
	}
	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, o := range open {
		if o.Key == stacks.GitDeployHeldNoticeKey("web") {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("held notices = %d, want 1", held)
	}

	if err := mgr.AcceptExternal(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(ctx, "web"); err != nil {
		t.Fatalf("sync after confirmation: %v", err)
	}
	if deploys != before+1 {
		t.Fatalf("deploys after confirmation = %d, want %d", deploys, before+1)
	}
	if detail, _ := mgr.Get("web"); !strings.Contains(detail.Compose, `REV: "2"`) {
		t.Fatal("commit not copied after confirmation")
	}
}

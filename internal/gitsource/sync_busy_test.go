package gitsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Syncs record their result and raise the held-back notice while other
// writers commit; none of those writes may fail with SQLITE_BUSY.
func TestSyncWhileOtherWritersCommit(t *testing.T) {
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

	svc := New(database, nil, mgr, t.TempDir(), nil)
	const count = 8
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("web%d", i)
		if _, err := svc.Configure(ctx, names[i], Input{RepoURL: repoDir}); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = database.Exec(`INSERT INTO notices (key, audience, severity, title) VALUES (?, 'all', 'info', 'noise')`,
				fmt.Sprintf("noise-%d", i))
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() {
		close(stop)
		writer.Wait()
	}()

	syncAll := func(want error) {
		t.Helper()
		errs := make(chan error, count)
		var wg sync.WaitGroup
		for _, name := range names {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := svc.Sync(ctx, name); !errors.Is(err, want) {
					errs <- fmt.Errorf("sync %s: %w", name, err)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	}
	syncAll(nil)

	for _, name := range names {
		dir, err := mgr.Dir(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("INJECTED=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commitCompose(t, repo, repoDir, "2")
	syncAll(stacks.ErrModifiedOutside)

	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held := map[string]bool{}
	for _, o := range open {
		held[o.Key] = true
	}
	for _, name := range names {
		if !held[stacks.GitDeployHeldNoticeKey(name)] {
			t.Errorf("no held-back notice for %s", name)
		}
	}
}

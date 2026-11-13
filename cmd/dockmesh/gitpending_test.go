package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type pendingHost struct {
	host.Host
	mu       sync.Mutex
	deployed []string
	fail     error
	notify   chan string
}

func (h *pendingHost) ID() string { return "local" }

func (h *pendingHost) DeployStack(_ context.Context, _, composeYAML, _ string) (*compose.DeployResult, error) {
	h.mu.Lock()
	h.deployed = append(h.deployed, composeYAML)
	err := h.fail
	h.mu.Unlock()
	h.notify <- composeYAML
	return &compose.DeployResult{}, err
}

func (h *pendingHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.deployed)
}

func (h *pendingHost) setFail(err error) {
	h.mu.Lock()
	h.fail = err
	h.mu.Unlock()
}

type pendingHosts struct{ h *pendingHost }

func (p pendingHosts) Pick(string) (host.Host, error) { return p.h, nil }

type allUsers struct{}

func (allUsers) ListActiveUserIDs(context.Context) ([]string, error) { return []string{"u1"}, nil }

type allowAll struct{}

func (allowAll) CanReceive(context.Context, string, notifications.EmitInput) bool { return true }

type pendingEnv struct {
	ctx     context.Context
	db      *sql.DB
	mgr     *stacks.Manager
	host    *pendingHost
	repo    *git.Repository
	repoDir string
	cache   string
	locks   *stacklock.Locks
	svc     *gitsource.Service
}

// newPendingEnv wires the git source service, the git deployer and the
// stack locks the way main does, for a webhook-only auto-deploy stack "web"
// that has been synced and deployed once.
func newPendingEnv(t *testing.T) *pendingEnv {
	t.Helper()
	e := &pendingEnv{ctx: context.Background(), host: &pendingHost{notify: make(chan string, 16)}}
	var err error
	if e.db, err = dbtest.Open(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.db.Close() })
	if _, err := e.db.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'u1', 'x')`); err != nil {
		t.Fatal(err)
	}
	if e.mgr, err = stacks.NewManager(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.mgr.Close() })
	e.repoDir = t.TempDir()
	e.repo, err = git.PlainInitWithOptions(e.repoDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.cache = t.TempDir()
	e.commit(t, "1")
	e.start(t)
	zero := 0
	if _, err := e.svc.Configure(e.ctx, "web", gitsource.Input{RepoURL: e.repoDir, AutoDeploy: true, PollIntervalSec: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Sync(e.ctx, "web"); err != nil {
		t.Fatal(err)
	}
	e.waitDeploy(t, `REV: "1"`)
	return e
}

// start builds a fresh lock table, deployer and service on the same
// database and stack files, as a server start does.
func (e *pendingEnv) start(t *testing.T) {
	t.Helper()
	notifs := notifications.NewService(e.db)
	notifs.SetUserDirectory(allUsers{})
	notifs.SetAccessChecker(allowAll{})
	e.locks = stacklock.New()
	var svc *gitsource.Service
	g := &gitDeployer{
		stacks:      e.mgr,
		deployments: stacks.NewDeploymentStore(e.db),
		history:     stacks.NewHistoryStore(e.db),
		hosts:       pendingHosts{e.host},
		notifs:      notifs,
		locks:       e.locks,
		lockWait:    50 * time.Millisecond,
		commit: func(ctx context.Context, stack string) string {
			if src, err := svc.Get(ctx, stack); err == nil {
				return src.LastSyncSHA
			}
			return ""
		},
		clearPending: func(ctx context.Context, stack string) error { return svc.ClearPending(ctx, stack) },
	}
	svc = gitsource.New(e.db, nil, e.mgr, e.cache, g.deploy)
	svc.SetDeployNow(g.deployNow)
	e.locks.OnRelease(func(k stacklock.Key) { svc.StackReleased(k.Stack) })
	e.svc = svc
}

func (e *pendingEnv) commit(t *testing.T, rev string) {
	t.Helper()
	compose := "services:\n  web:\n    image: nginx\n    environment:\n      REV: \"" + rev + "\"\n"
	if err := os.WriteFile(filepath.Join(e.repoDir, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	wt, err := e.repo.Worktree()
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

// busySync syncs while the stack is held by an image update: the commit
// must end up pending instead of deployed.
func (e *pendingEnv) busySync(t *testing.T) *gitsource.SyncResult {
	t.Helper()
	res, err := e.svc.Sync(e.ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deployed {
		t.Fatal("deployed although the stack was busy")
	}
	src, err := e.svc.Get(e.ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if src.PendingDeploy == nil || src.PendingDeploy.SHA != res.NewSHA || src.PendingDeploy.Operation != stacklock.OpImageUpdate {
		t.Fatalf("pending = %+v, want %s waiting for %s", src.PendingDeploy, res.NewSHA, stacklock.OpImageUpdate)
	}
	return res
}

func (e *pendingEnv) hold(t *testing.T) func() {
	t.Helper()
	release, err := e.locks.TryLock(stacklock.HostKey("local", "web"), stacklock.OpImageUpdate)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func (e *pendingEnv) waitDeploy(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-e.host.notify:
		if !strings.Contains(got, want) {
			t.Fatalf("deployed compose lacks %q:\n%s", want, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no deploy of %q", want)
	}
}

func (e *pendingEnv) noDeploy(t *testing.T) {
	t.Helper()
	select {
	case got := <-e.host.notify:
		t.Fatalf("unexpected deploy:\n%s", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func (e *pendingEnv) waitNoPending(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		src, err := e.svc.Get(e.ctx, "web")
		if err != nil {
			t.Fatal(err)
		}
		if src.PendingDeploy == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still pending: %+v", src.PendingDeploy)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGitCommitWhileBusyDeploysWhenStackFrees(t *testing.T) {
	e := newPendingEnv(t)
	release := e.hold(t)
	e.commit(t, "2")
	res := e.busySync(t)
	pending, err := e.svc.PendingDeploys(e.ctx)
	if err != nil || pending["web"] == nil || pending["web"].SHA != res.NewSHA {
		t.Fatalf("PendingDeploys = %v, %v", pending, err)
	}
	e.noDeploy(t)

	release()
	e.waitDeploy(t, `REV: "2"`)
	e.waitNoPending(t)
	e.noDeploy(t)
	entries, err := stacks.NewHistoryStore(e.db).List(e.ctx, "web", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].GitCommitSHA != res.NewSHA {
		t.Fatalf("history = %+v, want the pending commit %s on top", entries, res.NewSHA)
	}
}

func TestGitNewestPendingCommitWins(t *testing.T) {
	e := newPendingEnv(t)
	release := e.hold(t)
	e.commit(t, "2")
	first := e.busySync(t)
	e.commit(t, "3")
	second := e.busySync(t)
	if first.NewSHA == second.NewSHA {
		t.Fatal("both syncs saw the same commit")
	}

	release()
	e.waitDeploy(t, `REV: "3"`)
	e.waitNoPending(t)
	e.noDeploy(t)
	if n := e.host.count(); n != 2 {
		t.Fatalf("deploys = %d, want the initial one and the newest commit", n)
	}
}

func TestGitPendingCommitSurvivesRestart(t *testing.T) {
	e := newPendingEnv(t)
	e.hold(t)
	e.commit(t, "2")
	e.busySync(t)

	// The old lock table dies with the old process; its holder never releases.
	e.start(t)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.svc.Start(ctx)
	defer e.svc.Stop()
	e.waitDeploy(t, `REV: "2"`)
	e.waitNoPending(t)
}

func TestGitFailedPendingDeployIsReportedOnce(t *testing.T) {
	e := newPendingEnv(t)
	release := e.hold(t)
	e.commit(t, "2")
	e.busySync(t)
	e.host.setFail(errors.New("pull failed"))

	release()
	e.waitDeploy(t, `REV: "2"`)
	e.waitNoPending(t)
	var fails int
	deadline := time.Now().Add(5 * time.Second)
	for fails == 0 && time.Now().Before(deadline) {
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = ?`, notifications.KindDeployFail).Scan(&fails); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fails != 1 {
		t.Fatalf("deploy-fail notifications = %d, want 1", fails)
	}

	// Further lock releases must not retry the failed commit.
	e.hold(t)()
	e.noDeploy(t)
}

func TestGitPendingDroppedWhenAutoDeployTurnedOff(t *testing.T) {
	e := newPendingEnv(t)
	release := e.hold(t)
	e.commit(t, "2")
	e.busySync(t)
	zero := 0
	if _, err := e.svc.Configure(e.ctx, "web", gitsource.Input{RepoURL: e.repoDir, PollIntervalSec: &zero}); err != nil {
		t.Fatal(err)
	}
	e.waitNoPending(t)
	release()
	e.noDeploy(t)
}

func TestGitPendingClearedByManualDeploy(t *testing.T) {
	e := newPendingEnv(t)
	release := e.hold(t)
	e.commit(t, "2")
	e.busySync(t)
	// What a manual deploy does while it holds the stack.
	if err := e.svc.ClearPending(e.ctx, "web"); err != nil {
		t.Fatal(err)
	}
	release()
	e.noDeploy(t)
}

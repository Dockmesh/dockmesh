package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/deploy"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// gitDeployer runs the deploy after a git sync brought a new commit.
type gitDeployer struct {
	stacks      *stacks.Manager
	deployments *stacks.DeploymentStore
	history     *stacks.HistoryStore
	hosts       interface {
		Pick(id string) (host.Host, error)
	}
	globals deploy.GlobalVars
	notifs  *notifications.Service
	commit  func(ctx context.Context, stack string) string
	// clearPending drops the stack's pending commit once the deploy holds
	// the stack, since it deploys the stack's current files.
	clearPending func(ctx context.Context, stack string) error
	locks        *stacklock.Locks
	lockWait     time.Duration // gitDeployLockWait when zero
}

// gitDeployLockWait is how long a git deploy waits for another operation on
// the stack to finish before its commit is stored as pending. The wait stays
// short because it delays the polling of every other git stack and eats into
// the webhook sync's time budget.
const gitDeployLockWait = 2 * time.Minute

// deploy runs on the stack's deployment host, or the host it was created
// for, with the same globals and default override as a manual deploy.
func (g *gitDeployer) deploy(ctx context.Context, stackName string) (any, error) {
	hostID, err := g.hostOf(ctx, stackName)
	if err != nil {
		return nil, err
	}
	wait := g.lockWait
	if wait == 0 {
		wait = gitDeployLockWait
	}
	lockCtx, cancel := context.WithTimeout(ctx, wait)
	release, err := g.locks.Lock(lockCtx, stacklock.HostKey(hostID, stackName), stacklock.OpGitDeploy)
	cancel()
	if err != nil {
		return nil, err
	}
	defer release()
	return g.deployLocked(ctx, stackName, hostID)
}

// deployNow deploys a pending commit, or returns the *stacklock.BusyError
// when the stack is still busy.
func (g *gitDeployer) deployNow(ctx context.Context, stackName string) (any, error) {
	hostID, err := g.hostOf(ctx, stackName)
	if err != nil {
		return nil, err
	}
	release, err := g.locks.TryLock(stacklock.HostKey(hostID, stackName), stacklock.OpGitDeploy)
	if err != nil {
		return nil, err
	}
	defer release()
	return g.deployLocked(ctx, stackName, hostID)
}

func (g *gitDeployer) deployLocked(ctx context.Context, stackName, hostID string) (any, error) {
	// A pending commit left behind would deploy again on this release.
	if g.clearPending != nil {
		if err := g.clearPending(ctx, stackName); err != nil {
			return nil, fmt.Errorf("clear pending git deploy: %w", err)
		}
	}
	// Files and host are read under the lock, so a rollback or deploy that
	// ran while this waited is not undone with a stale snapshot.
	if now, err := g.hostOf(ctx, stackName); err != nil {
		return nil, err
	} else if now != hostID {
		return nil, fmt.Errorf("stack %s moved to host %s while the deploy waited", stackName, now)
	}
	if err := g.stacks.CheckGitDeploy(ctx, stackName); err != nil {
		return nil, err
	}
	snap, err := g.stacks.Snapshot(stackName)
	if err != nil {
		return nil, err
	}
	if len(snap.Changed) > 0 {
		if err := g.stacks.CheckGitDeploy(ctx, stackName); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", stacks.ErrModifiedOutside, strings.Join(snap.Changed, ", "))
	}
	detail := snap.Detail
	dir, err := g.stacks.Dir(stackName)
	if err != nil {
		return nil, err
	}
	gitSHA := ""
	if g.commit != nil {
		gitSHA = g.commit(ctx, stackName)
	}
	spec := deploy.Spec{}
	var res *compose.DeployResult
	start := time.Now()
	deployErr := func() error {
		target, err := g.hosts.Pick(hostID)
		if err != nil {
			return fmt.Errorf("host %s: %w", hostID, err)
		}
		if spec, err = deploy.ResolveSnapshot(ctx, g.globals, g.stacks, dir, snap); err != nil {
			return err
		}
		// Force-pull so a new push under the same tag is picked up even
		// when compose.yaml did not change.
		res, err = target.DeployStack(compose.WithForcePull(ctx), stackName, spec.Compose, spec.Env)
		return err
	}()
	durationMS := time.Since(start).Milliseconds()
	// A delete while the deploy ran must not leave a deployment row behind.
	if _, gerr := g.stacks.Get(stackName); deployErr == nil && gerr != nil {
		slog.Warn("git auto-deploy: stack deleted during the deploy", "stack", stackName)
		return nil, fmt.Errorf("stack %s was deleted during the deploy", stackName)
	}
	if deployErr == nil {
		if err := g.deployments.Set(ctx, stackName, hostID, "deployed"); err != nil {
			slog.Warn("git auto-deploy: set stack deployment", "stack", stackName, "host", hostID, "err", err)
		}
	}
	g.notify(ctx, stackName, hostID, gitSHA, res, deployErr)

	if g.history != nil {
		services := make([]stacks.DeployHistoryService, 0)
		if res != nil {
			for _, s := range res.Services {
				services = append(services, stacks.DeployHistoryService{Service: s.Name, Image: s.Image})
			}
		}
		errMsg := ""
		if deployErr != nil {
			errMsg = deployErr.Error()
		}
		if _, err := g.history.RecordGit(ctx, stackName, hostID, detail.Compose, spec.Override,
			services, deployErr == nil, durationMS, gitSHA, errMsg); err != nil {
			slog.Warn("git auto-deploy: record history failed", "stack", stackName, "err", err)
		}
	}
	if deployErr != nil {
		return nil, deployErr
	}
	return res, nil
}

func (g *gitDeployer) hostOf(ctx context.Context, stackName string) (string, error) {
	hostID, err := g.deployments.HostOf(ctx, stackName)
	if err != nil {
		return "", err
	}
	if hostID == "" {
		hostID = "local"
	}
	return hostID, nil
}

func (g *gitDeployer) notify(ctx context.Context, stackName, hostID, gitSHA string, res *compose.DeployResult, deployErr error) {
	if g.notifs == nil {
		return
	}
	in := notifications.EmitInput{
		Link:      "/stacks/" + stackName,
		Perm:      rbac.PermStacksView,
		HostID:    hostID,
		StackName: stackName,
	}
	switch {
	case deployErr != nil:
		in.Kind, in.Severity = notifications.KindDeployFail, notifications.SevError
		in.Title = "Git auto-deploy failed: " + stackName
		in.Body = deployErr.Error()
	case res != nil:
		in.Kind, in.Severity = notifications.KindDeployOK, notifications.SevSuccess
		in.Title = "Git auto-deploy: " + stackName
		in.Body = "stack synced from git"
		if len(gitSHA) >= 7 {
			in.Body = "synced to " + gitSHA[:7] + " — " + countServicesString(len(res.Services))
		}
	default:
		return
	}
	_, _ = g.notifs.Emit(ctx, in)
}

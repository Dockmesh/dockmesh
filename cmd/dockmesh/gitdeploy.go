package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/deploy"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
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
}

// deploy runs on the stack's deployment host, or the host it was created
// for, with the same globals and default override as a manual deploy.
func (g *gitDeployer) deploy(ctx context.Context, stackName string) (any, error) {
	if err := g.stacks.CheckGitDeploy(ctx, stackName); err != nil {
		return nil, err
	}
	detail, err := g.stacks.Get(stackName)
	if err != nil {
		return nil, err
	}
	dir, err := g.stacks.Dir(stackName)
	if err != nil {
		return nil, err
	}
	hostID, err := g.deployments.HostOf(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if hostID == "" {
		hostID = "local"
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
		if spec, err = deploy.Resolve(ctx, g.globals, dir, stackName, detail.Compose, detail.Env); err != nil {
			return err
		}
		// Force-pull so a new push under the same tag is picked up even
		// when compose.yaml did not change.
		res, err = target.DeployStack(compose.WithForcePull(ctx), stackName, spec.Compose, spec.Env)
		return err
	}()
	durationMS := time.Since(start).Milliseconds()
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

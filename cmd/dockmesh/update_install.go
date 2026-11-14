package main

import (
	"context"
	"os"
	"runtime"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// newUpdateInstaller wires the server side of the root updater and turns
// the result of an update that just ended into a notice.
func newUpdateInstaller(ctx context.Context, locks *stacklock.Locks, agentsSvc *agents.Service, n *notices.Service, auditSvc *audit.Service, channel func() string) (*selfupdate.Installer, error) {
	inst, err := selfupdate.NewInstaller(selfupdate.InstallerConfig{
		Dir:     config.ResolvePaths(os.Getenv).UpdateDir,
		Current: version.Version,
		Locks:   locks,
		Notices: n,
		Channel: channel,
		InContainer: proxy.DetectContainer(host.SelfContainerID),
		Audit: func(ctx context.Context, res rootupdate.Result) error {
			return auditUpdateResult(ctx, auditSvc, res)
		},
		Targets: func(ctx context.Context) ([]stacklock.Target, error) {
			list, err := agentsSvc.List(ctx)
			if err != nil {
				return nil, err
			}
			targets := []stacklock.Target{stacklock.Host("local")}
			for _, a := range list {
				targets = append(targets, stacklock.Host(a.ID))
			}
			return targets, nil
		},
		UpdaterInstalled: func() bool {
			if runtime.GOOS != "linux" {
				return false
			}
			_, err := os.Stat(layout.UpdatePathUnitFile)
			return err == nil
		},
	})
	if err != nil {
		return nil, err
	}
	go inst.AwaitResult(ctx)
	return inst, nil
}

// auditUpdateResult records the outcome of a server update as a system
// entry.
func auditUpdateResult(ctx context.Context, auditSvc *audit.Service, res rootupdate.Result) error {
	details := map[string]any{"from": res.From, "to": res.To, "step": res.Step, "reason": res.Reason}
	switch res.Status {
	case rootupdate.StatusUpdated:
		return auditSvc.WriteErr(ctx, "", audit.ActionUpdateApplied, res.To, details)
	case rootupdate.StatusRefused:
		return auditSvc.WriteErr(ctx, "", audit.ActionUpdateRefused, res.To, details)
	case rootupdate.StatusRolledBack:
		return auditSvc.WriteErr(ctx, "", audit.ActionUpdateRolledBack, res.To, details)
	case rootupdate.StatusFailed:
		return auditSvc.WriteErr(ctx, "", audit.ActionUpdateFailed, res.To, details)
	}
	return nil
}

// gitTargetMatcher reports whether a stack's git deploys run on target,
// so the end of a maintenance lock only retries pending deploys there.
func gitTargetMatcher(g *gitDeployer, target stacklock.Target) func(context.Context, string) bool {
	return func(ctx context.Context, stack string) bool {
		host, err := g.hostOf(ctx, stack)
		return err == nil && stacklock.Host(host) == target
	}
}

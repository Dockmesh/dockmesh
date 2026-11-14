package main

import (
	"context"
	"os"
	"runtime"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// newUpdateInstaller wires the server side of the root updater and turns
// the result of an update that just ended into a notice.
func newUpdateInstaller(ctx context.Context, locks *stacklock.Locks, agentsSvc *agents.Service, n *notices.Service) (*selfupdate.Installer, error) {
	inst, err := selfupdate.NewInstaller(selfupdate.InstallerConfig{
		Dir:     config.ResolvePaths(os.Getenv).UpdateDir,
		Current: version.Version,
		Locks:   locks,
		Notices: n,
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

// gitTargetMatcher reports whether a stack's git deploys run on target,
// so the end of a maintenance lock only retries pending deploys there.
func gitTargetMatcher(g *gitDeployer, target stacklock.Target) func(context.Context, string) bool {
	return func(ctx context.Context, stack string) bool {
		host, err := g.hostOf(ctx, stack)
		return err == nil && stacklock.Host(host) == target
	}
}

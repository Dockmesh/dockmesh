package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notices"
)

const (
	stagedMountNoticePrefix = "stacks.staged-bind-mount:"
	bindCopyNoticePrefix    = "stacks.bind-copy-failed:"
)

// syncStagedMountNotices raises one notice per stack whose containers
// still mount a deleted staging directory, and resolves the notices of
// stacks that no longer do (after a redeploy).
func syncStagedMountNotices(ctx context.Context, n *notices.Service, reg *host.Registry) error {
	staged, err := reg.StagedMountStacks(ctx)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, m := range staged {
		key := stagedMountNoticePrefix + m.HostID + "/" + m.Stack
		want[key] = true
		if err := n.Raise(ctx, notices.RaiseInput{
			Key:      key,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    fmt.Sprintf("Redeploy stack %s on %s", m.Stack, m.HostName),
			Body:     stagedMountBody(m),
			Link:     "/stacks/" + m.Stack,
		}); err != nil {
			return err
		}
	}
	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if strings.HasPrefix(o.Key, stagedMountNoticePrefix) && !want[o.Key] {
			if err := n.Resolve(ctx, o.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// stagedMountBody puts the agent upgrade first on agent hosts: an older
// agent stages the redeploy in a temporary directory again.
func stagedMountBody(m host.StagedMount) string {
	body := "This stack uses a relative bind mount (./path) and was deployed by an earlier " +
		"version that pointed it at a temporary directory. "
	if m.HostID != "" && m.HostID != "local" {
		body += fmt.Sprintf("Upgrade the agent first (Hosts → %s → Upgrade agent) and redeploy "+
			"the stack afterwards: ", m.HostName)
	} else {
		body += "Redeploy it: "
	}
	return body + "the mount then uses the stack directory, and files the container wrote are " +
		"copied there if it is empty."
}

// reportBindCopies raises a notice when data couldn't be moved out of an
// old staging mount during a deploy, and resolves it once a deploy of the
// stack has nothing left to report.
func reportBindCopies(ctx context.Context, n *notices.Service, hostID, stack string, res *compose.DeployResult) {
	var failed []string
	for _, c := range res.BindCopies {
		if c.Error != "" {
			failed = append(failed, fmt.Sprintf("%s:%s (%s)", c.Service, c.Target, c.Error))
		}
	}
	err := n.Set(ctx, len(failed) > 0, notices.RaiseInput{
		Key:      bindCopyNoticePrefix + hostID + "/" + stack,
		Audience: notices.Admins(),
		Severity: notices.SevCritical,
		Title:    "Data from an old bind mount of " + stack + " was not copied",
		Body: "The redeploy could not copy files the old container wrote into its temporary bind " +
			"mount: " + strings.Join(failed, "; ") + ". The old container is gone; check the " +
			"server log and restore the data from a backup if needed.",
		Link: "/stacks/" + stack,
	})
	if err != nil {
		slog.Warn("bind copy notice", "stack", stack, "err", err)
	}
}

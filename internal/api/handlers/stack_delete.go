package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// agentStackDelete is the outcome of removing a stack's copy on its agent.
type agentStackDelete int

const (
	// agentDeleteNone: the stack is not on an agent, or the agent was
	// removed or revoked and will never connect again.
	agentDeleteNone agentStackDelete = iota
	agentDeleteDone
	// agentDeletePending: the agent is offline or dropped during the delete.
	agentDeletePending
)

// deleteStackOnAgent removes the agent's copy of a stack and waits for the
// answer. It also returns the host's display name.
func (h *Handlers) deleteStackOnAgent(ctx context.Context, hostID, name string) (agentStackDelete, string, error) {
	if hostID == "" || hostID == "local" || h.Agents == nil {
		return agentDeleteNone, hostID, nil
	}
	a, err := h.Agents.Get(ctx, hostID)
	if errors.Is(err, agents.ErrNotFound) {
		return agentDeleteNone, hostID, nil
	}
	if err != nil {
		return agentDeleteNone, hostID, err
	}
	if a.Status == "revoked" {
		return agentDeleteNone, a.Name, nil
	}
	ag := h.Agents.GetConnected(hostID)
	if ag == nil {
		return agentDeletePending, a.Name, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentDeleteTimeout)
	defer cancel()
	if _, err := ag.Request(ctx, agents.Frame{
		Type:    agents.FrameReqStackDelete,
		Payload: mustJSON(agents.StackNameReq{Name: name}),
	}); err != nil {
		if errors.Is(err, agents.ErrAgentGone) {
			return agentDeletePending, a.Name, nil
		}
		return agentDeleteNone, a.Name, err
	}
	return agentDeleteDone, a.Name, nil
}

// finishStackDelete removes the stack's files on the server and every row
// that refers to it. With missingOK, files that are already gone still
// count as deleted.
func (h *Handlers) finishStackDelete(ctx context.Context, name string, missingOK bool) error {
	// A client that disconnects must not stop the delete half-way.
	ctx = context.WithoutCancel(ctx)
	if err := h.Stacks.Delete(ctx, name); err != nil && (!missingOK || !errors.Is(err, stacks.ErrNotFound)) {
		return err
	}
	if h.Deployments != nil {
		if err := h.Deployments.Delete(ctx, name); err != nil {
			slog.Warn("delete stack deployment row", "stack", name, "err", err)
		}
		if err := h.Deployments.DeleteTarget(ctx, name); err != nil {
			slog.Warn("drop stack target host", "stack", name, "err", err)
		}
	}
	// A git source left behind would re-create the stack on its next commit.
	if h.GitSource != nil {
		if err := h.GitSource.Delete(ctx, name); err != nil && !errors.Is(err, gitsource.ErrNotFound) {
			slog.Warn("delete stack git source", "stack", name, "err", err)
		}
	}
	// Stacks that depended on this one fail fast on their next deploy with
	// "dependency missing", which leaves the decision to the operator.
	if h.Dependencies != nil {
		if err := h.Dependencies.DeleteAll(ctx, name); err != nil {
			slog.Warn("delete stack dependency edges", "stack", name, "err", err)
		}
	}
	return nil
}

const stackDeleteNoticePrefix = "stack.delete_failed:"

// ResumePendingStackDeletes finishes the stack deletes that waited for
// hostID. It runs when an agent connects; a failure raises a notice with
// the agent's error and leaves the delete pending for the next connect.
func (h *Handlers) ResumePendingStackDeletes(ctx context.Context, hostID string) {
	if h.Deployments == nil {
		return
	}
	if _, running := h.pendingDeleteRuns.LoadOrStore(hostID, struct{}{}); running {
		return
	}
	defer h.pendingDeleteRuns.Delete(hostID)
	deps, err := h.Deployments.ByHost(ctx, hostID)
	if err != nil {
		slog.Warn("pending stack deletes", "host", hostID, "err", err)
		return
	}
	for _, d := range deps {
		if d.Status != stacks.DeploymentDeletePending {
			continue
		}
		if !h.resumeStackDelete(ctx, hostID, d.StackName) {
			return
		}
	}
}

// resumeStackDelete finishes one pending delete under the stack lock; a
// busy stack stays pending. It returns false when the agent went offline
// again.
func (h *Handlers) resumeStackDelete(ctx context.Context, hostID, name string) bool {
	release, err := h.StackLocks.TryLock(stacklock.HostKey(hostID, name), stacklock.OpDelete)
	if err != nil {
		return true
	}
	defer release()
	if !h.deletePending(ctx, name, hostID) {
		return true
	}
	if err := h.stopBeforePendingDelete(ctx, hostID, name); err != nil {
		h.raiseStackDeleteNotice(ctx, name, h.hostDisplayName(ctx, hostID), "stop: "+agentErrorText(err))
		return true
	}
	state, hostName, err := h.deleteStackOnAgent(ctx, hostID, name)
	if err != nil {
		h.raiseStackDeleteNotice(ctx, name, hostName, agentErrorText(err))
		return true
	}
	if state == agentDeletePending {
		return false
	}
	if !h.deletePending(ctx, name, hostID) {
		return true
	}
	if err := h.finishStackDelete(ctx, name, true); err != nil {
		h.raiseStackDeleteNotice(ctx, name, "Dockmesh server", err.Error())
		return true
	}
	if h.Notices != nil {
		if err := h.Notices.Resolve(ctx, stackDeleteNoticePrefix+name); err != nil {
			slog.Warn("resolve stack delete notice", "stack", name, "err", err)
		}
	}
	if h.Audit != nil {
		h.Audit.Write(ctx, "", audit.ActionStackDelete, name, map[string]string{"completed_on": hostName})
	}
	slog.Info("pending stack delete finished", "stack", name, "host", hostName)
	return true
}

// stopBeforePendingDelete stops the stack's containers on hostID when the
// user asked for that with the delete.
func (h *Handlers) stopBeforePendingDelete(ctx context.Context, hostID, name string) error {
	stop, err := h.Deployments.DeleteStops(ctx, name)
	if err != nil || !stop || h.Hosts == nil {
		return err
	}
	target, err := h.Hosts.Pick(hostID)
	if err != nil {
		return err
	}
	return target.StopStack(ctx, name)
}

func (h *Handlers) hostDisplayName(ctx context.Context, hostID string) string {
	if a, err := h.Agents.Get(ctx, hostID); err == nil {
		return a.Name
	}
	return hostID
}

// deletePending reports whether name still waits for its delete on hostID;
// a deploy since then cancelled it.
func (h *Handlers) deletePending(ctx context.Context, name, hostID string) bool {
	cur, err := h.Deployments.Get(ctx, name)
	return err == nil && cur != nil && cur.Status == stacks.DeploymentDeletePending && cur.HostID == hostID
}

func (h *Handlers) raiseStackDeleteNotice(ctx context.Context, name, hostName, reason string) {
	slog.Warn("pending stack delete failed", "stack", name, "host", hostName, "err", reason)
	if h.Notices == nil {
		return
	}
	if err := h.Notices.Raise(ctx, notices.RaiseInput{
		Key:      stackDeleteNoticePrefix + name,
		Audience: notices.WithPermission(string(rbac.PermStacksDelete)),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.stack_delete_failed.title", name),
		Body:     i18n.Msg("notice.stack_delete_failed.body", name, hostName, reason),
		Link:     "/stacks/" + name,
	}); err != nil {
		slog.Warn("stack delete notice", "stack", name, "err", err)
	}
}

// agentErrorText is an agent's error as shown to users: one line, without
// control characters, at most 512 bytes. The agent controls the text.
func agentErrorText(err error) string {
	msg := strings.TrimPrefix(err.Error(), "agent: ")
	msg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > 512 {
		cut := 512
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	return msg
}

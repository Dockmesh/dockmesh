package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

// UnitNoticeKey is the notice raised when a unit rewrite did not go
// through.
const UnitNoticeKey = "unit.rewrite.result"

// Unit rewrite phases reported by UnitRewriteState.
const (
	UnitPhaseIdle       = "idle"
	UnitPhaseWaiting    = "waiting_for_stacks"
	UnitPhaseRestarting = "restarting"
	UnitPhaseFailed     = "failed"
)

// ErrUnitRewriteRunning refuses a second rewrite, or an update, while one
// is pending.
var ErrUnitRewriteRunning = errors.New("a restart to allow new backup directories is already pending")

// unitResultWait bounds how long a rewrite keeps the stacks held after
// root took the request: root restarts this process within seconds, so
// still running after this means it never did.
var unitResultWait = 5 * time.Minute

// UnitRewriteState is what the server knows about the unit rewrite it
// asked for.
type UnitRewriteState struct {
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
	// RestartPending is set from the request until the restart.
	RestartPending bool `json:"restart_pending"`
	// Unavailable names why StartUnitRewrite would refuse.
	Unavailable string `json:"unavailable,omitempty"`
	// LastResult is root's report of the last rewrite this process read.
	LastResult *rootupdate.UnitResult `json:"last_result,omitempty"`
}

func (i *Installer) unitBusyLocked() bool {
	return i.unit.Phase == UnitPhaseWaiting || i.unit.Phase == UnitPhaseRestarting
}

// UnitState returns the state of the unit rewrite started last.
func (i *Installer) UnitState() UnitRewriteState {
	i.mu.Lock()
	st := i.unit
	if st.Phase == "" {
		st.Phase = UnitPhaseIdle
	}
	if st.LastResult != nil {
		res := *st.LastResult
		st.LastResult = &res
	}
	i.mu.Unlock()
	st.RestartPending = st.Phase == UnitPhaseWaiting || st.Phase == UnitPhaseRestarting
	st.Unavailable = i.unavailable()
	return st
}

func (i *Installer) setUnit(phase string, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.unit.Phase = phase
	i.unit.Error = ""
	if err != nil {
		i.unit.Error = err.Error()
	}
}

// StartUnitRewrite asks the root updater to write the service unit again
// and restart the server, once no stack operation runs. It refuses while
// an update is installed or pending, and on hosts without the updater.
func (i *Installer) StartUnitRewrite(ctx context.Context) error {
	switch i.unavailable() {
	case UnavailableContainer:
		return ErrInContainer
	case UnavailableNoUpdater:
		return ErrUpdaterMissing
	}
	if i.UpdatePending() {
		return ErrInstallRunning
	}
	i.mu.Lock()
	switch {
	case i.unitBusyLocked():
		i.mu.Unlock()
		return ErrUnitRewriteRunning
	case i.state.Phase == PhaseDownloading || i.state.Phase == PhaseWaiting || i.state.Phase == PhaseApplying:
		i.mu.Unlock()
		return ErrInstallRunning
	}
	i.unit.Phase, i.unit.Error = UnitPhaseWaiting, ""
	i.mu.Unlock()
	go i.runUnitRewrite(context.WithoutCancel(ctx))
	return nil
}

func (i *Installer) runUnitRewrite(ctx context.Context) {
	if err := i.unitRewrite(ctx); err != nil {
		slog.Warn("unit rewrite failed", "err", err)
		i.setUnit(UnitPhaseFailed, err)
	}
}

func (i *Installer) unitRewrite(ctx context.Context) error {
	var targets []stacklock.Target
	if i.cfg.Targets != nil {
		var err error
		if targets, err = i.cfg.Targets(ctx); err != nil {
			return fmt.Errorf("list the hosts to hold: %w", err)
		}
	}
	// The restart cuts off whatever the server drives, as an update does.
	release, err := i.cfg.Locks.Maintenance(ctx, targets, stacklock.OpServerUpdate, i.cfg.LockWait)
	if err != nil {
		slog.Warn("unit rewrite waits for stacks", "err", err)
		return errStacksStillBusy
	}
	defer release()
	if _, err := os.Lstat(filepath.Join(i.cfg.Dir, rootupdate.RequestFile)); err == nil {
		return ErrInstallRunning
	}
	if err := removeFile(i.cfg.Dir, rootupdate.UnitResultFile); err != nil {
		return err
	}
	req, _ := json.Marshal(rootupdate.Request{Type: rootupdate.RequestUnitRewrite})
	if err := writeFileAtomic(i.cfg.Dir, rootupdate.RequestFile, req); err != nil {
		return err
	}
	i.setUnit(UnitPhaseRestarting, nil)
	slog.Info("unit rewrite requested")
	deadline := time.Now().Add(i.cfg.ApplyWait)
	var taken time.Time
	for {
		if res, ok := i.readUnitResult(); ok && res.Status != rootupdate.UnitRestarting {
			// Root refused before it restarted anything.
			i.CheckUnitResult(ctx)
			if res.Status == rootupdate.UnitDone {
				i.setUnit(UnitPhaseIdle, nil)
				return nil
			}
			return fmt.Errorf("the unit rewrite was %s: %s", res.Status, res.Reason)
		}
		_, statErr := os.Lstat(filepath.Join(i.cfg.Dir, rootupdate.RequestFile))
		now := time.Now()
		switch {
		case statErr == nil && now.After(deadline):
			_ = removeFile(i.cfg.Dir, rootupdate.RequestFile)
			return errUpdaterNoAnswer
		case statErr != nil && taken.IsZero():
			taken = now
		case !taken.IsZero() && now.Sub(taken) > unitResultWait:
			return errors.New("the root updater took the request but did not restart the server")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (i *Installer) readUnitResult() (rootupdate.UnitResult, bool) {
	var res rootupdate.UnitResult
	data, err := readSmallFile(i.cfg.Dir, rootupdate.UnitResultFile, 64<<10)
	if err != nil || json.Unmarshal(data, &res) != nil {
		return res, false
	}
	return res, true
}

// AwaitUnitResult reads root's report of a unit rewrite once it is final.
// The server calls it on start: right after the restart, root still waits
// for this process to report healthy.
func (i *Installer) AwaitUnitResult(ctx context.Context) {
	deadline := time.Now().Add(rootupdate.DefaultHealthTimeout + time.Minute)
	for {
		res, ok := i.readUnitResult()
		if !ok || res.Status != rootupdate.UnitRestarting || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pendingPoll):
		}
	}
	i.CheckUnitResult(ctx)
}

// CheckUnitResult turns root's report of a unit rewrite into a notice for
// the admins when it did not go through, removes the report and returns
// it (zero without one).
func (i *Installer) CheckUnitResult(ctx context.Context) rootupdate.UnitResult {
	data, err := readSmallFile(i.cfg.Dir, rootupdate.UnitResultFile, 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return rootupdate.UnitResult{}
	}
	var res rootupdate.UnitResult
	if err == nil {
		err = json.Unmarshal(data, &res)
	}
	if err != nil {
		slog.Warn("read unit rewrite result", "err", err)
		_ = removeFile(i.cfg.Dir, rootupdate.UnitResultFile)
		return rootupdate.UnitResult{}
	}
	if res.Status == rootupdate.UnitRestarting {
		res = rootupdate.UnitResult{Status: rootupdate.UnitFailed, Step: "health", Reason: "root never reported the end of the restart"}
	}
	slog.Info("unit rewrite result", "status", res.Status, "step", res.Step, "reason", res.Reason)
	i.mu.Lock()
	last := res
	i.unit.LastResult = &last
	i.mu.Unlock()
	if i.cfg.Notices != nil {
		if err := i.cfg.Notices.Resolve(ctx, UnitNoticeKey); err != nil {
			slog.Warn("resolve unit rewrite notice", "err", err)
		}
		if res.Status != rootupdate.UnitDone {
			if err := i.cfg.Notices.Raise(ctx, notices.RaiseInput{
				Key:      UnitNoticeKey,
				Audience: notices.Admins(),
				Severity: notices.SevWarning,
				Title:    i18n.Msg("notice.unit_rewrite.title"),
				Body:     i18n.Msg("notice.unit_rewrite.body", res.Reason, "sudo dockmesh system write-unit && sudo systemctl restart dockmesh"),
			}); err != nil {
				slog.Warn("raise unit rewrite notice", "err", err)
			}
		}
	}
	_ = removeFile(i.cfg.Dir, rootupdate.UnitResultFile)
	return res
}

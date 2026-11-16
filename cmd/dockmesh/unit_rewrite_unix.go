//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// unitService is what a unit rewrite needs from systemd and the server.
type unitService interface {
	ReadUnit() ([]byte, error)
	RestoreUnit(data []byte) error
	DaemonReload(ctx context.Context) error
	// WriteUnit writes the unit from the stored settings, refusing it
	// whole when one value fails the unit writer's checks.
	WriteUnit(ctx context.Context) error
	Restart(ctx context.Context) error
	Healthy(ctx context.Context) bool
}

// unitRewriter is root's side of a unit-rewrite request: write the unit
// again so directories added to the settings become writable, restart,
// and put the old unit back if the server does not come up.
type unitRewriter struct {
	reqDir string
	uid    uint32
	svc    unitService
	log    *slog.Logger
	// healthTimeout and poll default to the updater's.
	healthTimeout time.Duration
	poll          time.Duration
	// settle gives the page that asked a moment to see the restart coming.
	settle time.Duration
	// lock defaults to lockSetup, so a setup-apply never runs alongside.
	lock func() (func(), error)
}

func (u *unitRewriter) result(status, step string, err error) {
	r := rootupdate.UnitResult{Status: status, Step: step, Finished: time.Now().UTC()}
	if err != nil {
		r.Reason = err.Error()
	}
	data, _ := json.Marshal(r)
	if werr := rootupdate.WriteServiceFile(u.reqDir, u.uid, rootupdate.UnitResultFile, data); werr != nil {
		u.log.Error("unit rewrite result not written", "err", werr)
	}
	if err != nil {
		u.log.Error("unit rewrite "+status, "step", step, "err", err)
	} else {
		u.log.Info("unit rewrite " + status)
	}
}

// run handles one unit-rewrite request. The request is removed first, so
// the path unit does not start it again.
func (u *unitRewriter) run(ctx context.Context) error {
	if err := rootupdate.RemoveServiceFile(u.reqDir, u.uid, rootupdate.RequestFile); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(u.reqDir, rootupdate.PendingFile)); err == nil {
		u.result(rootupdate.UnitRefused, "request", errors.New("an update is in flight"))
		return nil
	}
	lock := u.lock
	if lock == nil {
		lock = lockSetup
	}
	unlock, err := lock()
	if err != nil {
		u.result(rootupdate.UnitFailed, "lock", err)
		return nil
	}
	defer unlock()
	prev, err := u.svc.ReadUnit()
	if err != nil {
		u.result(rootupdate.UnitRefused, "unit", err)
		return nil
	}
	if err := u.svc.WriteUnit(ctx); err != nil {
		// A refused value leaves the unit as it was; a failure after the
		// write must not leave a half-applied one either.
		if rerr := errors.Join(u.svc.RestoreUnit(prev), u.svc.DaemonReload(ctx)); rerr != nil {
			err = fmt.Errorf("%w; restoring the unit failed: %w", err, rerr)
		}
		u.result(rootupdate.UnitRefused, "unit", err)
		return nil
	}
	u.result(rootupdate.UnitRestarting, "restart", nil)
	sleepCtx(ctx, u.settle)
	if err := u.svc.Restart(ctx); err != nil {
		u.rollBack(ctx, prev, "restart", err)
		return nil
	}
	if !u.waitHealthy(ctx) {
		u.rollBack(ctx, prev, "health", errors.New("the server did not report healthy with the new unit"))
		return nil
	}
	u.result(rootupdate.UnitDone, "", nil)
	return nil
}

func (u *unitRewriter) waitHealthy(ctx context.Context) bool {
	timeout, poll := u.healthTimeout, u.poll
	if timeout == 0 {
		timeout = rootupdate.DefaultHealthTimeout
	}
	if poll == 0 {
		poll = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for !u.svc.Healthy(ctx) {
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		sleepCtx(ctx, poll)
	}
	return true
}

func (u *unitRewriter) rollBack(ctx context.Context, prev []byte, step string, cause error) {
	// Rolling back still has to happen when systemd stops the oneshot.
	ctx = context.WithoutCancel(ctx)
	err := errors.Join(u.svc.RestoreUnit(prev), u.svc.DaemonReload(ctx), u.svc.Restart(ctx))
	if err == nil && u.waitHealthy(ctx) {
		u.result(rootupdate.UnitRolledBack, step, cause)
		return
	}
	if err == nil {
		err = errors.New("the server did not report healthy with the old unit either")
	}
	u.result(rootupdate.UnitFailed, step, fmt.Errorf("%w; the rollback did not finish: %w", cause, err))
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// serverUnitService is unitService for the installed server.
type serverUnitService struct {
	*serverService
}

func (s serverUnitService) WriteUnit(context.Context) error {
	_, err := writeServerUnit("") //nolint:contextcheck // the unit writer runs its own short-lived children
	return err
}

func (s serverUnitService) Restart(ctx context.Context) error {
	return systemctl(ctx, "restart", serverUnitName)
}

// Healthy reads the listener from the config on every probe, so a probe
// set up before the restart never pins a stale certificate.
func (s serverUnitService) Healthy(ctx context.Context) bool {
	envFile, err := unitConfigFile("")
	if err != nil {
		return false
	}
	env, err := layout.ReadEnvFile(envFile)
	if err != nil {
		return false
	}
	health, client, err := healthProbe(env, config.ResolvePaths(func(k string) string { return env[k] }))
	if err != nil {
		return false
	}
	probe := &serverService{health: health, client: client}
	return probe.Healthy(ctx, version.Version)
}

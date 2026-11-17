package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// HelperTimeout bounds stopping and removing a short-lived helper
// container, so its cleanup can't hang on a context nobody cancels.
const HelperTimeout = time.Minute

// HelperLogConfig keeps a helper's stdout out of Docker's log: the helpers
// stream whole archives there, which the json-file driver would store
// again, several times their size.
var HelperLogConfig = container.LogConfig{Type: "none"}

// HelperAPI is the part of the Docker client FinishHelper needs.
type HelperAPI interface {
	ContainerKill(ctx context.Context, id string, opts client.ContainerKillOptions) (client.ContainerKillResult, error)
	ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// FinishHelper waits for a helper container to exit and removes it. With
// aborted set the reader went away, so the helper is killed first: it would
// otherwise keep running with nobody reading its output. It runs on its
// own deadline, also when ctx is already done.
func FinishHelper(ctx context.Context, cli HelperAPI, id string, aborted bool) (int64, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), HelperTimeout)
	defer cancel()
	defer func() { _, _ = cli.ContainerRemove(cctx, id, client.ContainerRemoveOptions{Force: true}) }()
	if aborted {
		_, _ = cli.ContainerKill(cctx, id, client.ContainerKillOptions{Signal: "KILL"})
	}
	wait := cli.ContainerWait(cctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case s := <-wait.Result:
		return s.StatusCode, nil
	case err := <-wait.Error:
		return -1, err
	case <-cctx.Done():
		return -1, cctx.Err()
	}
}

// BackupHelperComponent is the dockmesh.component label of the helpers
// that tar volumes in and out for backups and restores.
const BackupHelperComponent = "backup-helper"

// HelperSweepAPI is the part of the Docker client RemoveOrphanHelpers needs.
type HelperSweepAPI interface {
	ContainerList(ctx context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// RemoveOrphanHelpers removes the stopped backup helpers created before
// startedAt, the start of this process: a helper outlives its job only
// when the process that ran it died or lost the daemon before removing
// it. Running helpers stay, because an agent on the same daemon may
// still be streaming through one.
func RemoveOrphanHelpers(ctx context.Context, cli HelperSweepAPI, startedAt time.Time) (int, error) {
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "dockmesh.component="+BackupHelperComponent),
	})
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, c := range list.Items {
		// Created has whole seconds, so a helper of this process can
		// carry the second it started in; only earlier ones are orphans.
		if c.Labels["dockmesh.component"] != BackupHelperComponent ||
			c.Created >= startedAt.Unix() || !stoppedState(c.State) {
			continue
		}
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			errs = append(errs, fmt.Errorf("remove helper %s: %w", c.ID, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// SweepOrphanHelpers runs RemoveOrphanHelpers and logs the outcome.
func SweepOrphanHelpers(ctx context.Context, cli HelperSweepAPI, startedAt time.Time) {
	n, err := RemoveOrphanHelpers(ctx, cli, startedAt)
	if n > 0 {
		slog.Info("removed backup helpers left by an earlier run", "count", n)
	}
	if err != nil {
		slog.Warn("remove leftover backup helpers", "err", err)
	}
}

func stoppedState(s container.ContainerState) bool {
	switch s {
	case container.StateCreated, container.StateExited, container.StateDead:
		return true
	}
	return false
}

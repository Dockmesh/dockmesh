package docker

import (
	"context"
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

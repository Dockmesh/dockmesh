package docker

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
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
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerWait(ctx context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error
}

// FinishHelper waits for a helper container to exit and removes it. With
// aborted set the reader went away, so the helper is killed first: it would
// otherwise keep running with nobody reading its output. It runs on its
// own deadline, also when ctx is already done.
func FinishHelper(ctx context.Context, cli HelperAPI, id string, aborted bool) (int64, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), HelperTimeout)
	defer cancel()
	defer func() { _ = cli.ContainerRemove(cctx, id, container.RemoveOptions{Force: true}) }()
	if aborted {
		_ = cli.ContainerKill(cctx, id, "KILL")
	}
	statusCh, errCh := cli.ContainerWait(cctx, id, container.WaitConditionNotRunning)
	select {
	case s := <-statusCh:
		return s.StatusCode, nil
	case err := <-errCh:
		return -1, err
	case <-cctx.Done():
		return -1, cctx.Err()
	}
}

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/operations"
)

// idleTimeout fails a run when no bytes move between source and target
// for this long. Transfers that keep making progress never hit it, however
// long they take.
var idleTimeout = 10 * time.Minute

// ErrTargetStalled ends a run whose transfer stopped moving.
var ErrTargetStalled = errors.New("target stalled")

// activity remembers when bytes last moved.
type activity struct{ last atomic.Int64 }

func newActivity() *activity {
	a := &activity{}
	a.touch()
	return a
}

func (a *activity) touch() {
	if a != nil {
		a.last.Store(time.Now().UnixNano())
	}
}

func (a *activity) idle() time.Duration {
	return time.Since(time.Unix(0, a.last.Load()))
}

// watchIdle cancels the run with ErrTargetStalled once a has been idle
// for idleTimeout. The returned func stops watching.
func watchIdle(a *activity, stop context.CancelCauseFunc) func() {
	limit := idleTimeout
	every := limit / 20
	if every < 10*time.Millisecond {
		every = 10 * time.Millisecond
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if a.idle() >= limit {
					stop(fmt.Errorf("%w: no data moved for %s", ErrTargetStalled, humanDuration(limit)))
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func humanDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		if m := int(d / time.Minute); m != 1 {
			return fmt.Sprintf("%d minutes", m)
		}
		return "1 minute"
	}
	return d.String()
}

// stopCause returns why ctx ended when a user cancelled the run or the
// transfer stalled; the errors the interrupted calls return ("context
// canceled", "use of closed connection") say neither.
func stopCause(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	c := context.Cause(ctx)
	if errors.Is(c, operations.ErrCancelled) || errors.Is(c, ErrTargetStalled) {
		return c
	}
	return nil
}

// touchReader marks activity on every read that returns data.
type touchReader struct {
	r   io.Reader
	act *activity
}

func (t touchReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.act.touch()
	}
	return n, err
}

// closeOnDone closes c when ctx ends, which unblocks a read or write that
// does not watch a context itself (SFTP, SMB, agent streams). The returned
// func stops watching.
func closeOnDone(ctx context.Context, c func() error) func() bool {
	return context.AfterFunc(ctx, func() { _ = c() })
}

// stoppableHost closes the volume streams it hands out when their context
// ends, so a stalled or cancelled source gives up its read.
type stoppableHost struct {
	hostBackupTarget
}

func (h stoppableHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, err := h.hostBackupTarget.VolumeTar(ctx, name)
	if err != nil {
		return nil, err
	}
	stop := closeOnDone(ctx, rc.Close)
	return &stoppableReader{ReadCloser: rc, stop: stop}, nil
}

type stoppableReader struct {
	io.ReadCloser
	stop func() bool
}

func (r *stoppableReader) Close() error {
	r.stop()
	return r.ReadCloser.Close()
}

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/dockmesh/dockmesh/internal/agents"
)

// Errors for agents that predate an optional frame.
var (
	ErrEventsUnsupported = errors.New("agent too old to stream docker events")
	ErrMountsUnsupported = errors.New("agent too old to report disk mounts")
)

// ContainerEvents streams the host's Docker container events until ctx
// ends or the daemon connection drops; the channel is then closed.
func (h *LocalHost) ContainerEvents(ctx context.Context) (<-chan events.Message, error) {
	if h.cli == nil {
		return nil, errors.New("docker unavailable")
	}
	msgs, errs := h.cli.Raw().Events(ctx, events.ListOptions{
		Filters: filters.NewArgs(filters.Arg("type", string(events.ContainerEventType))),
	})
	out := make(chan events.Message, 16)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errs:
				if err != nil && ctx.Err() == nil {
					slog.Debug("local docker events ended", "err", err)
				}
				return
			case m := <-msgs:
				select {
				case out <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// ContainerEvents streams the agent's Docker container events.
func (h *RemoteHost) ContainerEvents(ctx context.Context) (<-chan events.Message, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	if !ag.Hello.Has(agents.CapDockerEvents) {
		return nil, ErrEventsUnsupported
	}
	stream, err := ag.OpenStream(ctx, "events", "", nil)
	if err != nil {
		return nil, err
	}
	out := make(chan events.Message, 16)
	go func() {
		<-ctx.Done()
		_ = stream.Close()
	}()
	go func() {
		defer close(out)
		budget := &readBudget{r: stream, left: maxEventBytes}
		dec := json.NewDecoder(budget)
		for {
			var m events.Message
			err := dec.Decode(&m)
			budget.left = maxEventBytes
			if err != nil {
				if ctx.Err() == nil {
					slog.Debug("agent docker events ended", "host", h.name, "err", err)
				}
				return
			}
			select {
			case out <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// maxEventBytes bounds one event from an agent: the decoder would
// otherwise keep buffering a value that never ends.
const maxEventBytes = 64 << 10

var errEventTooLarge = errors.New("agent docker event too large")

type readBudget struct {
	r    io.Reader
	left int
}

func (b *readBudget) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errEventTooLarge
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= n
	return n, err
}

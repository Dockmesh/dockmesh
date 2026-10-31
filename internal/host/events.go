package host

import (
	"context"
	"encoding/json"
	"errors"
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
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	if !h.agent.Hello.Has(agents.CapDockerEvents) {
		return nil, ErrEventsUnsupported
	}
	stream, err := h.agent.OpenStream(ctx, "events", "", nil)
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
		dec := json.NewDecoder(stream)
		for {
			var m events.Message
			if err := dec.Decode(&m); err != nil {
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

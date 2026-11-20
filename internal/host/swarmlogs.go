package host

import (
	"context"
	"errors"
	"io"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/swarmread"
)

// ErrAgentNoSwarmLogs: the agent predates the service log stream.
var ErrAgentNoSwarmLogs = errors.New("the agent is too old to stream Swarm service logs; upgrade it")

// SwarmServiceLogs streams req.Service's task logs as SwarmLogLine NDJSON
// from the local daemon, which must be a Swarm manager.
func (h *LocalHost) SwarmServiceLogs(ctx context.Context, req agents.SwarmLogsReq) (io.ReadCloser, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		defer cancel()
		pw.CloseWithError(swarmread.WriteServiceLogs(ctx, h.cli.Raw(), req, pw))
	}()
	return &cancelReader{ReadCloser: pr, cancel: cancel}, nil
}

// SwarmServiceLogs opens the agent's service log stream.
func (h *RemoteHost) SwarmServiceLogs(ctx context.Context, req agents.SwarmLogsReq) (io.ReadCloser, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	if !ag.Hello.Has(agents.CapSwarmLogs) {
		return nil, ErrAgentNoSwarmLogs
	}
	s, err := ag.OpenStream(ctx, agents.StreamKindSwarmServiceLogs, req.Service, req.Params())
	if err != nil {
		return nil, err
	}
	return &swarmStream{ReadCloser: s}, nil
}

// CheckSwarmLogs fails when host id can't stream service logs: an agent
// that is offline or predates the stream.
func (r *Registry) CheckSwarmLogs(id string) error {
	h, err := r.Pick(id)
	if err != nil {
		return err
	}
	rh, ok := h.(*RemoteHost)
	if !ok {
		return nil
	}
	ag, err := rh.live()
	if err != nil {
		return err
	}
	if !ag.Hello.Has(agents.CapSwarmLogs) {
		return ErrAgentNoSwarmLogs
	}
	return nil
}

// SwarmServiceLogs streams a service's logs through manager host id.
func (r *Registry) SwarmServiceLogs(ctx context.Context, id string, req agents.SwarmLogsReq) (io.ReadCloser, error) {
	h, err := r.Pick(id)
	if err != nil {
		return nil, err
	}
	l, ok := h.(interface {
		SwarmServiceLogs(context.Context, agents.SwarmLogsReq) (io.ReadCloser, error)
	})
	if !ok {
		return nil, ErrUnknownHost
	}
	return l.SwarmServiceLogs(ctx, req)
}

type cancelReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReader) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}

// swarmStream turns an agent's "not a manager" or "not found" at the end
// of the stream back into the sentinel.
type swarmStream struct{ io.ReadCloser }

func (s *swarmStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		for _, e := range []error{agents.ErrNotSwarmManager, agents.ErrSwarmServiceNotFound} {
			if err.Error() == e.Error() {
				return n, e
			}
		}
	}
	return n, err
}

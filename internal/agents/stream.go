package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

var (
	ErrStreamClosed   = errors.New("stream closed")
	ErrStreamOverflow = errors.New("stream buffer overflow: the agent sent data faster than it was read (agent update recommended)")
)

// Stream is a long-lived bidirectional channel between the central server
// and the agent's docker daemon. It's used by logs / stats / exec — anywhere
// request/response doesn't fit because the data is chunked over time or
// flows in both directions.
//
// Stream implements io.ReadCloser so existing handlers that consume
// `*docker.Client.ContainerLogs(...)` can be plugged onto a remote stream
// with no other changes (the demux / line-scanning code is identical).
//
// With agents that support CapStreamCredit both directions are flow
// controlled: the agent may have at most StreamWindow frames unread here,
// and the server waits for credit before sending more than StreamWindow
// frames of stdin. Older agents get no flow control; if they outrun the
// reader the stream fails with ErrStreamOverflow instead of losing data.
type Stream struct {
	ID       string
	agent    *ConnectedAgent
	incoming chan []byte
	closed   atomic.Bool

	done       chan struct{}
	finishOnce sync.Once
	err        error // terminal error, written before done closes; nil = EOF

	credit     bool    // flow control negotiated with the agent
	sendCredit *Credit // server → agent frames we may still send

	// Only Read touches these.
	pending  []byte
	consumed int
}

// OpenStream tells the agent to start producing data for the given kind
// (logs / stats / exec) and returns a Stream the caller can read from.
// The stream stays open until Close() is called or the agent ends it.
func (c *ConnectedAgent) OpenStream(ctx context.Context, kind, container string, params map[string]any) (*Stream, error) {
	id := uuid.NewString()
	s := &Stream{
		ID:       id,
		agent:    c,
		incoming: make(chan []byte, StreamWindow),
		done:     make(chan struct{}),
	}
	if c.Hello.Has(CapStreamCredit) {
		s.credit = true
		s.sendCredit = NewCredit(StreamWindow)
		withWindow := make(map[string]any, len(params)+1)
		for k, v := range params {
			withWindow[k] = v
		}
		withWindow["window"] = StreamWindow
		params = withWindow
	}

	c.streamsMu.Lock()
	if c.streams == nil {
		c.streams = make(map[string]*Stream)
	}
	c.streams[id] = s
	c.streamsMu.Unlock()

	payload, _ := json.Marshal(StreamOpen{
		StreamID:  id,
		Kind:      kind,
		Container: container,
		Params:    params,
	})
	if err := c.enqueue(ctx, Frame{Type: FrameStreamOpen, Payload: payload}); err != nil {
		c.removeStream(id)
		return nil, err
	}
	return s, nil
}

// finish ends the stream with err (nil = clean EOF). The first call wins.
func (s *Stream) finish(err error) {
	s.finishOnce.Do(func() {
		s.err = err
		close(s.done)
	})
}

func (s *Stream) endErr() error {
	if s.err != nil {
		return s.err
	}
	return io.EOF
}

// WriteControl sends an out-of-band control message (e.g. exec resize)
// to the agent side.
func (s *Stream) WriteControl(op string, params map[string]any) error {
	if s.closed.Load() {
		return io.ErrClosedPipe
	}
	payload, _ := json.Marshal(StreamControl{StreamID: s.ID, Op: op, Params: params})
	return s.agent.enqueue(context.Background(), Frame{Type: FrameStreamControl, Payload: payload})
}

// WriteFrame sends a stream.data frame in the server → agent direction
// (exec keystrokes, volume import data). It blocks while the agent has
// no credit left instead of dropping data.
func (s *Stream) WriteFrame(data []byte) error {
	if s.closed.Load() {
		return io.ErrClosedPipe
	}
	select {
	case <-s.done:
		return s.endErr()
	default:
	}
	if s.sendCredit != nil {
		if err := s.sendCredit.Acquire(context.Background(), s.done); err != nil {
			return s.endErr()
		}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	payload, _ := json.Marshal(StreamData{StreamID: s.ID, Data: cp})
	return s.agent.enqueue(context.Background(), Frame{Type: FrameStreamData, Payload: payload})
}

// CloseWrite ends the server → agent direction. The agent finishes its
// side (e.g. waits for tar) and reports the outcome as the error Read
// returns at the end.
func (s *Stream) CloseWrite() error {
	return s.WriteControl(StreamOpEOF, nil)
}

// Read implements io.Reader. Blocks until the next chunk arrives, the
// stream ends or the agent disconnects. Data that arrived before the
// end is returned first.
func (s *Stream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}

	var chunk []byte
	select {
	case chunk = <-s.incoming:
	case <-s.done:
		select {
		case chunk = <-s.incoming:
		default:
			return 0, s.endErr()
		}
	case <-s.agent.done:
		select {
		case chunk = <-s.incoming:
		default:
			return 0, ErrAgentGone
		}
	}
	if err := s.grant(); err != nil {
		return 0, err
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		s.pending = append(s.pending, chunk[n:]...)
	}
	return n, nil
}

// grant returns credit for consumed frames in batches. A credit frame
// that can't be queued would stall the agent for good, so that fails
// the stream.
func (s *Stream) grant() error {
	if !s.credit {
		return nil
	}
	s.consumed++
	if s.consumed < GrantBatch {
		return nil
	}
	n := s.consumed
	s.consumed = 0
	payload, _ := json.Marshal(StreamControl{StreamID: s.ID, Op: StreamOpCredit, Params: map[string]any{"n": n}})
	if err := s.agent.enqueue(context.Background(), Frame{Type: FrameStreamControl, Payload: payload}); err != nil {
		s.finish(err)
		return err
	}
	return nil
}

// Close ends the stream. Safe to call multiple times.
func (s *Stream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.finish(ErrStreamClosed)
	s.agent.removeStream(s.ID)
	payload, _ := json.Marshal(StreamClose{StreamID: s.ID})
	// The agent must learn about the close: with flow control its
	// producer would otherwise wait for credit forever.
	_ = s.agent.enqueue(context.Background(), Frame{Type: FrameStreamClose, Payload: payload})
	return nil
}

// -----------------------------------------------------------------------------
// Routing — called by the WS read loop in server.go when a stream frame
// arrives from the agent. Must never block.
// -----------------------------------------------------------------------------

func (c *ConnectedAgent) routeStreamFrame(f Frame) {
	switch f.Type {
	case FrameStreamData:
		var sd StreamData
		if err := json.Unmarshal(f.Payload, &sd); err != nil {
			return
		}
		s := c.lookupStream(sd.StreamID)
		if s == nil {
			return
		}
		if len(sd.Data) > maxStreamChunk {
			s.failOverflow()
			return
		}
		select {
		case s.incoming <- sd.Data:
		default:
			s.failOverflow()
		}

	case FrameStreamControl:
		var sc StreamControl
		if err := json.Unmarshal(f.Payload, &sc); err != nil {
			return
		}
		if s := c.lookupStream(sc.StreamID); s != nil && sc.Op == StreamOpCredit && s.sendCredit != nil {
			s.sendCredit.Add(CreditCount(sc.Params))
		}

	case FrameStreamClose:
		var sc StreamClose
		_ = json.Unmarshal(f.Payload, &sc)
		s := c.lookupStream(sc.StreamID)
		if s == nil {
			return
		}
		c.removeStream(s.ID)
		if sc.Error != "" {
			s.finish(errors.New(sc.Error))
		} else {
			s.finish(nil)
		}
	}
}

// maxStreamChunk bounds one data frame, so the window also bounds the
// memory a stream can hold. Agents send at most 32 KiB per frame.
const maxStreamChunk = 256 << 10

// failOverflow ends the stream loudly and tells the agent to stop.
func (s *Stream) failOverflow() {
	s.finish(ErrStreamOverflow)
	s.agent.removeStream(s.ID)
	payload, _ := json.Marshal(StreamClose{StreamID: s.ID, Error: ErrStreamOverflow.Error()})
	s.agent.Send(Frame{Type: FrameStreamClose, Payload: payload})
}

func (c *ConnectedAgent) lookupStream(id string) *Stream {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	return c.streams[id]
}

func (c *ConnectedAgent) removeStream(id string) {
	c.streamsMu.Lock()
	delete(c.streams, id)
	c.streamsMu.Unlock()
}

// closeAllStreams is called when the agent disconnects. Readers get
// ErrAgentGone rather than EOF, so a cut-off archive is never mistaken
// for a complete one.
func (c *ConnectedAgent) closeAllStreams() {
	c.streamsMu.Lock()
	all := c.streams
	c.streams = nil
	c.streamsMu.Unlock()
	for _, s := range all {
		s.finish(ErrAgentGone)
	}
}

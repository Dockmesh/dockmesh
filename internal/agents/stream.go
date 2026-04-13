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

// Stream is a long-lived bidirectional channel between the central server
// and the agent's docker daemon. It's used by logs / stats / exec — anywhere
// request/response doesn't fit because the data is chunked over time or
// flows in both directions.
//
// Stream implements io.ReadCloser so existing handlers that consume
// `*docker.Client.ContainerLogs(...)` can be plugged onto a remote stream
// with no other changes (the demux / line-scanning code is identical).
type Stream struct {
	ID       string
	agent    *ConnectedAgent
	incoming chan []byte
	closeErr error
	closed   atomic.Bool

	// Holds bytes left over from the previous Read call when the caller's
	// buffer was smaller than the most recent chunk.
	mu      sync.Mutex
	pending []byte
}

// OpenStream tells the agent to start producing data for the given kind
// (logs / stats / exec) and returns a Stream the caller can read from.
// The stream stays open until Close() is called or the agent ends it.
func (c *ConnectedAgent) OpenStream(ctx context.Context, kind, container string, params map[string]any) (*Stream, error) {
	id := uuid.NewString()
	s := &Stream{
		ID:       id,
		agent:    c,
		incoming: make(chan []byte, 64),
	}

	c.streamsMu.Lock()
	if c.streams == nil {
		c.streams = make(map[string]*Stream)
	}
	c.streams[id] = s
	c.streamsMu.Unlock()

	open := StreamOpen{
		StreamID:  id,
		Kind:      kind,
		Container: container,
		Params:    params,
	}
	payload, _ := json.Marshal(open)

	select {
	case c.send <- Frame{Type: FrameStreamOpen, Payload: payload}:
	case <-ctx.Done():
		c.removeStream(id)
		return nil, ctx.Err()
	}
	return s, nil
}

// WriteFrame sends a stream.data frame in the agent → server direction
// from outside the package (e.g. exec keystroke forwarding).
func (s *Stream) WriteFrame(data []byte) error {
	if s.closed.Load() {
		return io.ErrClosedPipe
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	payload, _ := json.Marshal(StreamData{StreamID: s.ID, Data: cp})
	select {
	case s.agent.send <- Frame{Type: FrameStreamData, Payload: payload}:
		return nil
	default:
		return errors.New("agent send buffer full")
	}
}

// Read implements io.Reader. Blocks until the next chunk arrives, the
// stream is closed, or the underlying agent disconnects.
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	chunk, ok := <-s.incoming
	if !ok {
		if s.closeErr != nil {
			return 0, s.closeErr
		}
		return 0, io.EOF
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		s.mu.Lock()
		s.pending = append(s.pending, chunk[n:]...)
		s.mu.Unlock()
	}
	return n, nil
}

// Close ends the stream. Safe to call multiple times.
func (s *Stream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	payload, _ := json.Marshal(StreamClose{StreamID: s.ID})
	select {
	case s.agent.send <- Frame{Type: FrameStreamClose, Payload: payload}:
	default:
	}
	s.agent.removeStream(s.ID)
	return nil
}

// -----------------------------------------------------------------------------
// Routing — called by the WS read loop in server.go when a stream frame
// arrives from the agent.
// -----------------------------------------------------------------------------

func (c *ConnectedAgent) routeStreamFrame(f Frame) {
	switch f.Type {
	case FrameStreamData:
		var sd StreamData
		if err := json.Unmarshal(f.Payload, &sd); err != nil {
			return
		}
		c.streamsMu.Lock()
		s, ok := c.streams[sd.StreamID]
		c.streamsMu.Unlock()
		if !ok {
			return
		}
		select {
		case s.incoming <- sd.Data:
		default:
			// Slow consumer — drop. For logs this surfaces as a gap in the
			// UI; for exec it would be visible as missing characters.
			// Increasing the buffer in OpenStream is the fix.
		}

	case FrameStreamClose:
		var sc StreamClose
		_ = json.Unmarshal(f.Payload, &sc)
		c.streamsMu.Lock()
		s, ok := c.streams[sc.StreamID]
		if ok {
			delete(c.streams, sc.StreamID)
		}
		c.streamsMu.Unlock()
		if ok {
			if sc.Error != "" {
				s.closeErr = errors.New(sc.Error)
			}
			close(s.incoming)
			s.closed.Store(true)
		}
	}
}

func (c *ConnectedAgent) removeStream(id string) {
	c.streamsMu.Lock()
	s, ok := c.streams[id]
	if ok {
		delete(c.streams, id)
	}
	c.streamsMu.Unlock()
	if ok {
		// Best-effort drain so a blocked Read returns EOF.
		select {
		case <-s.incoming:
		default:
		}
		// Defer closing the channel — if the caller is still reading we
		// don't want to close-twice panic. The caller is expected to call
		// Close() which is idempotent.
	}
}

// closeAllStreams is called when the agent disconnects so blocked readers
// in handler goroutines unblock with EOF.
func (c *ConnectedAgent) closeAllStreams() {
	c.streamsMu.Lock()
	all := c.streams
	c.streams = nil
	c.streamsMu.Unlock()
	for _, s := range all {
		if !s.closed.Swap(true) {
			close(s.incoming)
		}
	}
}

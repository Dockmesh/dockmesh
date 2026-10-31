package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// serverSilence is how long the agent waits without any frame before
	// it treats the server as gone. The server pings every 30s.
	serverSilence = 90 * time.Second
	// stableSession is how long a session must last for the next
	// reconnect to start over at the immediate first attempt.
	stableSession = time.Minute
	maxBackoff    = 60 * time.Second
)

// agentCapabilities is what this agent advertises in its hello.
var agentCapabilities = []string{
	agents.CapRequestCancel, agents.CapStreamCredit, agents.CapDiskMounts, agents.CapDockerEvents,
}

// reconnectDelay is the wait before reconnect attempt n (0-based): the
// first retry is immediate, then exponential from 1s up to maxBackoff,
// each with ±20% jitter so a fleet doesn't reconnect in lockstep.
func reconnectDelay(attempt int, r *rand.Rand) time.Duration {
	if attempt <= 0 {
		return 0
	}
	d := maxBackoff
	if attempt <= 7 {
		d = min(time.Second<<(attempt-1), maxBackoff)
	}
	jitter := 0.8 + 0.4*r.Float64()
	return time.Duration(float64(d) * jitter)
}

// inflight tracks running server requests so req.cancel can stop them.
var inflight = struct {
	sync.Mutex
	m map[string]context.CancelFunc
}{m: map[string]context.CancelFunc{}}

func trackRequest(id string, cancel context.CancelFunc) {
	inflight.Lock()
	inflight.m[id] = cancel
	inflight.Unlock()
}

func untrackRequest(id string) {
	inflight.Lock()
	delete(inflight.m, id)
	inflight.Unlock()
}

func cancelRequest(id string) {
	inflight.Lock()
	cancel := inflight.m[id]
	delete(inflight.m, id)
	inflight.Unlock()
	if cancel != nil {
		cancel()
	}
}

// outgoing holds agent → server requests waiting for their res frame.
var outgoing = struct {
	sync.Mutex
	m map[string]chan agents.Frame
}{m: map[string]chan agents.Frame{}}

func deliverResponse(f agents.Frame) {
	outgoing.Lock()
	ch := outgoing.m[f.ID]
	outgoing.Unlock()
	if ch != nil {
		select {
		case ch <- f:
		default:
		}
	}
}

// askServer sends an agent → server request and waits for the answer.
func askServer(ctx context.Context, conn *websocket.Conn, frameType string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	ch := make(chan agents.Frame, 1)
	outgoing.Lock()
	outgoing.m[id] = ch
	outgoing.Unlock()
	defer func() {
		outgoing.Lock()
		delete(outgoing.m, id)
		outgoing.Unlock()
	}()
	if err := writeFrame(conn, agents.Frame{Type: frameType, ID: id, Payload: raw}); err != nil {
		return err
	}
	select {
	case f := <-ch:
		var env agents.ResponseEnvelope
		if err := json.Unmarshal(f.Payload, &env); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if !env.OK {
			return errors.New(env.Error)
		}
		return json.Unmarshal(env.Data, out)
	case <-ctx.Done():
		return ctx.Err()
	}
}

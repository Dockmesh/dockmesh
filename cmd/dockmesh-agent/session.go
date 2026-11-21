package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/labtime"
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
	agents.CapVolumeReplace, agents.CapMetricsHistory, agents.CapVolumeStaged, agents.CapContainerFiles,
	agents.CapVolumeManage, agents.CapResourceManage, agents.CapVolumeFileDownload, agents.CapImageTransfer,
	agents.CapImageHistory, agents.CapNoEnvMirror, agents.CapNetworkTopology, agents.CapReservedLabels, agents.CapStackFiles,
	agents.CapSwarmNodes, agents.CapSwarmRead, agents.CapSwarmLogs, agents.CapSwarmProve,
	agents.CapSwarmWrite,
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

// removedRetry is the reconnect interval once the server has refused the
// host's certificate for removedGrace; until then the normal backoff runs,
// so a server briefly started on the wrong database doesn't park the fleet.
var (
	removedRetry = labtime.Tick(time.Hour)
	removedGrace = labtime.Tick(10 * time.Minute)
)

// errHostRemoved is the server's refusal of a certificate it has no host
// for: the host was deleted in Dockmesh.
var errHostRemoved = errors.New("this host was removed from Dockmesh")

// dialError turns a refused websocket handshake into errHostRemoved when
// the server answered that it has no host for this certificate.
func dialError(resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusUnauthorized && strings.Contains(string(body), "agent not registered") {
		return errHostRemoved
	}
	return fmt.Errorf("dial: %w (HTTP %d)", err, resp.StatusCode)
}

// reconnects decides the wait before the next connection attempt; a
// deleted host is logged once.
type reconnects struct {
	attempt int
	// removedSince is when the server first refused the host as deleted;
	// zero otherwise.
	removedSince time.Time
	now          func() time.Time
}

func (r *reconnects) quiet() bool { return !r.removedSince.IsZero() }

func (r *reconnects) next(err error, session time.Duration, rng *rand.Rand) time.Duration {
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	if !errors.Is(err, errHostRemoved) {
		r.removedSince = time.Time{}
	} else if r.removedSince.IsZero() {
		slog.Error("This host was removed from Dockmesh. Run: sudo dockmesh-agent uninstall " +
			"(if you re-issued its token instead, run the install command again)")
		r.removedSince = now()
	}
	if session >= stableSession {
		r.attempt = 0
	}
	if r.quiet() && now().Sub(r.removedSince) >= removedGrace {
		return time.Duration(float64(removedRetry) * (0.8 + 0.4*rng.Float64()))
	}
	delay := reconnectDelay(r.attempt, rng)
	r.attempt++
	if err != nil && !r.quiet() {
		slog.Warn("disconnected, reconnecting", "err", err, "backoff", delay)
	}
	return delay
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

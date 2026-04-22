// Package docker wraps the Docker Go SDK with connection-state tracking.
//
// The Docker daemon isn't always available when dockmesh starts up —
// on macOS the boot sequence routinely has launchd firing our service
// before Docker Desktop has opened its socket, and Docker daemons can
// also be restarted independently in the running case. The wrapper
// here turns that into a recoverable condition:
//
//   - Client construction never blocks on a ping. Even if the socket
//     doesn't exist yet, we return a valid *Client; the SDK fails
//     individual requests with a dial error until the socket appears.
//   - A background goroutine polls Ping every `probeInterval` seconds
//     and flips an atomic "connected" flag. Handlers and the /health
//     endpoint read that flag cheaply instead of hammering the socket.
//   - When Docker comes back up, the flag flips without anyone having
//     to restart dockmesh. The SDK client's internal HTTP transport
//     reuses the socket on the next successful dial.
package docker

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

const (
	// How often the background monitor probes the daemon. 10s is a
	// good trade-off: fast enough that the UI banner clears within
	// one refresh cycle after Docker starts, slow enough to not spam
	// the socket on healthy systems.
	probeInterval = 10 * time.Second

	// Each ping gets this long before giving up. Short so boot-time
	// "socket not there yet" cases are detected quickly and don't
	// stall the monitor loop.
	probeTimeout = 2 * time.Second
)

type Client struct {
	cli *client.Client

	// atomic.Bool is set by the monitor goroutine and read by every
	// caller of Connected() — cheap, no mutex.
	connected atomic.Bool

	// lastErr holds the most recent probe error as a string for the
	// health endpoint. atomic.Pointer[string] keeps the read-side
	// branch-free.
	lastErr atomic.Pointer[string]

	stop chan struct{}
}

// New creates the underlying Docker SDK client but does NOT verify
// connectivity. A daemon that isn't up yet (macOS launchd race at
// boot, Docker Desktop still starting, daemon restart in the middle
// of the day) is a *recoverable* state — the monitor picks it up
// within a probeInterval and handlers switch back to "connected"
// automatically.
func New(ctx context.Context) (*Client, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		// Only fail here if the client CAN'T be constructed (invalid
		// env vars, bad host URL). Daemon availability is tracked
		// separately.
		return nil, fmt.Errorf("docker client: %w", err)
	}
	c := &Client{cli: cli, stop: make(chan struct{})}

	// First probe is synchronous so startup logs reflect the actual
	// initial state. Even if it fails, we proceed — the monitor will
	// retry.
	c.probe(ctx)

	go c.monitor()
	return c, nil
}

// probe pings the daemon once and updates connected + lastErr.
func (c *Client) probe(ctx context.Context) {
	pingCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := c.cli.Ping(pingCtx); err != nil {
		c.connected.Store(false)
		s := err.Error()
		c.lastErr.Store(&s)
		return
	}
	c.connected.Store(true)
	c.lastErr.Store(nil)
}

// monitor loops forever until Close, re-probing on a fixed interval.
// Uses a fresh context per probe so the probe timeout is enforced
// per-attempt rather than cumulatively.
func (c *Client) monitor() {
	t := time.NewTicker(probeInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.probe(context.Background())
		}
	}
}

// Connected reports whether the most recent probe succeeded. Safe to
// call concurrently; a single atomic load, no syscall.
func (c *Client) Connected() bool { return c.connected.Load() }

// LastError returns the most recent probe error message (empty string
// when connected or before the first probe completed). For the health
// endpoint and the UI banner.
func (c *Client) LastError() string {
	p := c.lastErr.Load()
	if p == nil {
		return ""
	}
	return *p
}

// Ping is a synchronous health check for callers that want the
// authoritative answer right now (the health endpoint on boot, for
// example). Updates the internal state as a side effect so the next
// Connected() call reflects the result.
func (c *Client) Ping(ctx context.Context) error {
	c.probe(ctx)
	if !c.connected.Load() {
		return fmt.Errorf("%s", c.LastError())
	}
	return nil
}

// Info returns the daemon's view of its resource limits + state. On
// macOS + Windows, NCPU and MemTotal are the Docker Desktop VM's
// configured limits (what the operator picked in Settings → Resources),
// NOT the host hardware totals. On Linux they're cgroup-aware and
// typically equal to the host totals unless dockmesh runs inside a
// constrained container. dockmesh's dashboard uses these as the
// authoritative "resources Docker can use" numbers.
func (c *Client) Info(ctx context.Context) (dtypes.Info, error) {
	return c.cli.Info(ctx)
}

func (c *Client) Close() error {
	select {
	case <-c.stop:
		// already closed
	default:
		close(c.stop)
	}
	if c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// Raw exposes the underlying Docker SDK client. Used by compose, backup,
// updater, proxy and metrics subsystems. Typed per-resource wrappers were
// considered in Phase 1 but deferred — the SDK surface is large and the
// value of wrapping every call is marginal compared to the churn. Keep.
func (c *Client) Raw() *client.Client { return c.cli }

// Wrap adopts an existing low-level docker SDK client. Used by the agent
// where we already have a *client.Client from the heartbeat / request
// handlers and don't want to open a second connection just to satisfy
// the compose.Service constructor.
func Wrap(cli *client.Client) *Client {
	c := &Client{cli: cli, stop: make(chan struct{})}
	// Agent-side we're already known-connected because the agent only
	// wraps the client after its own daemon probe succeeded. Start in
	// connected=true and let the monitor confirm.
	c.connected.Store(true)
	go c.monitor()
	return c
}

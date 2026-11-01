// dockmesh-agent is the remote-host companion to the central dockmesh
// server. It connects outbound via mTLS and tells the server about the
// local docker daemon. Concept §3.1.
//
// Usage (typical):
//
//	docker run -d --name dockmesh-agent --restart unless-stopped \
//	  -v /var/run/docker.sock:/var/run/docker.sock \
//	  -v dockmesh-agent:/var/lib/dockmesh \
//	  -v ./agents-ca.crt:/etc/dockmesh/enroll-ca.pem:ro \
//	  -e DOCKMESH_ENROLL_URL=https://main:8443/enroll \
//	  -e DOCKMESH_AGENT_URL=wss://main:8443/connect \
//	  -e DOCKMESH_TOKEN=<token-from-admin-ui> \
//	  -e DOCKMESH_CA_FINGERPRINT=<ca-fingerprint-from-admin-ui> \
//	  -e DOCKMESH_ENROLL_CA_FILE=/etc/dockmesh/enroll-ca.pem \
//	  ghcr.io/dockmesh/agent:latest
//
// On first start the agent calls the enroll URL with its token, verifying
// the server against the pinned agents CA, persists the returned cert +
// key under DATA_DIR, and then dials the agent URL over mTLS. On reboot
// it skips the enroll and goes straight to dial.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"time"

	dockerwrap "github.com/dockmesh/dockmesh/internal/docker"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/system"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/pkg/version"
)

type agentConfig struct {
	dataDir        string
	enrollURL      string
	agentURL       string
	token          string
	caFingerprint  string
	enrollCAFile   string
	insecureEnroll bool
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			fmt.Printf("dockmesh-agent %s %s/%s (commit %s, built %s)\n",
				version.Version, runtime.GOOS, runtime.GOARCH, version.Commit, version.Date)
			return
		case "status":
			runStatusCmd()
			return
		}
	}
	insecureEnroll := os.Getenv("DOCKMESH_INSECURE_ENROLL") == "true"
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--insecure-enroll":
			insecureEnroll = true
		default:
			fmt.Fprintf(os.Stderr, "unknown argument: %s\n", arg)
			os.Exit(2)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := agentConfig{
		dataDir:        dataDir(),
		enrollURL:      os.Getenv("DOCKMESH_ENROLL_URL"),
		agentURL:       os.Getenv("DOCKMESH_AGENT_URL"),
		token:          os.Getenv("DOCKMESH_TOKEN"),
		caFingerprint:  os.Getenv("DOCKMESH_CA_FINGERPRINT"),
		enrollCAFile:   os.Getenv("DOCKMESH_ENROLL_CA_FILE"),
		insecureEnroll: insecureEnroll,
	}

	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		slog.Error("data dir", "err", err)
		os.Exit(1)
	}
	legacyOpenBinDir(cfg.dataDir)

	certPath := filepath.Join(cfg.dataDir, "agent.crt")
	keyPath := filepath.Join(cfg.dataDir, "agent.key")
	caPath := filepath.Join(cfg.dataDir, "ca.crt")
	urlPath := filepath.Join(cfg.dataDir, "agent.url")

	// Step 1: enrollment, only if no cert on disk yet.
	if !fileExists(certPath) || !fileExists(keyPath) || !fileExists(caPath) {
		if cfg.enrollURL == "" || cfg.token == "" {
			slog.Error("first run: DOCKMESH_ENROLL_URL and DOCKMESH_TOKEN are required")
			os.Exit(1)
		}
		slog.Info("enrolling", "url", cfg.enrollURL)
		if err := enroll(cfg, certPath, keyPath, caPath, urlPath); err != nil {
			slog.Error("enrollment failed", "err", err)
			os.Exit(1)
		}
		slog.Info("enrolled successfully")
	}

	// Resolve the agent URL: env override > persisted file > error.
	dialURL := cfg.agentURL
	if dialURL == "" {
		if b, err := os.ReadFile(urlPath); err == nil {
			dialURL = string(bytes.TrimSpace(b))
		}
	}
	if dialURL == "" {
		slog.Error("DOCKMESH_AGENT_URL not set and no persisted url found")
		os.Exit(1)
	}

	if err := finishPendingRenewal(certPath, keyPath); err != nil {
		slog.Error("finish cert renewal", "err", err)
		os.Exit(1)
	}
	store, err := newCertStore(certPath, keyPath, caPath)
	if err != nil {
		slog.Error("tls config", "err", err)
		os.Exit(1)
	}
	if time.Now().After(store.notAfter()) {
		slog.Error("client certificate expired: re-issue the enrollment token on the host page and re-run "+
			"the installer on this host (the host keeps its ID)", "expired", store.notAfter())
		if cfg.token != "" && cfg.enrollURL != "" {
			if err := enroll(cfg, certPath, keyPath, caPath, urlPath); err != nil {
				slog.Error("re-enrollment with the configured token failed", "err", err)
			} else if err := store.load(); err != nil {
				slog.Error("load re-enrolled cert", "err", err)
				os.Exit(1)
			} else {
				slog.Info("re-enrolled with a new client certificate")
			}
		}
	}
	tlsCfg := store.tlsConfig()
	upgradeOrigin.dialURL = dialURL
	upgradeOrigin.tls = tlsCfg

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		slog.Info("shutting down")
		cancel()
	}()

	// Host-metrics sampler: mirrors the central server so every host
	// (local and every remote agent) emits CPU% averaged over a 5s
	// window. Without this the per-host rows on the dashboard would
	// jitter between 15% and 20% each poll while local already
	// reports a smoothed value.
	system.StartSampler(ctx)

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(os.Getpid()))) //nolint:gosec // reconnect jitter, not a secret
	attempt := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := runOnce(ctx, dialURL, tlsCfg, store)
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) >= stableSession {
			attempt = 0
		}
		delay := reconnectDelay(attempt, rng)
		attempt++
		if err != nil {
			slog.Warn("disconnected, reconnecting", "err", err, "backoff", delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func runOnce(ctx context.Context, dialURL string, tlsCfg *tls.Config, store *certStore) error {
	u, err := url.Parse(dialURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	dialer := websocket.Dialer{
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: 15 * time.Second,
	}
	slog.Info("dialing", "url", u.String())
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(agents.MaxFrameBytes)

	// Send hello
	hostname, _ := os.Hostname()
	dockerVersion := dockerDaemonVersion(ctx)
	hello := agents.HelloPayload{
		Version:         version.Version,
		Commit:          version.Commit,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Hostname:        hostname,
		DockerVersion:   dockerVersion,
		ProtocolVersion: agents.ProtocolVersion,
		Capabilities:    agentCapabilities,
	}
	helloBytes, _ := json.Marshal(hello)
	if err := writeFrame(conn, agents.Frame{Type: agents.FrameAgentHello, Payload: helloBytes}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	// Wait for welcome
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("welcome read: %w", err)
	}
	var f agents.Frame
	if err := json.Unmarshal(raw, &f); err != nil || f.Type != agents.FrameServerWelcome {
		return fmt.Errorf("expected welcome, got %q", f.Type)
	}
	var welcome agents.WelcomePayload
	_ = json.Unmarshal(f.Payload, &welcome)
	slog.Info("connected", "agent_id", welcome.AgentID, "agent_name", welcome.AgentName,
		"server", welcome.ServerVersion, "protocol", welcome.ProtocolVersion)

	_ = conn.SetReadDeadline(time.Now().Add(serverSilence))

	ctxConn, cancel := context.WithCancel(ctx)
	defer cancel()
	// A failed write closes the socket so the read loop doesn't wait on
	// a dead connection.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctxConn.Done():
				return
			case <-t.C:
				count := dockerContainerCount(ctxConn)
				hb, _ := json.Marshal(agents.HeartbeatPayload{TS: time.Now().Unix(), ContainerCount: count})
				if err := writeFrame(conn, agents.Frame{Type: agents.FrameAgentHeartbeat, Payload: hb}); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	if slices.Contains(welcome.Capabilities, agents.CapCertRenew) {
		go renewLoop(ctxConn, conn, store)
	} else if time.Until(store.notAfter()) < agents.RenewBefore {
		slog.Warn("client cert expires soon and this server can't renew it; update the server",
			"expires", store.notAfter())
	}

	// One docker client and compose service per connection. The wrapper
	// runs a health monitor that only stops on Close.
	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		slog.Warn("docker client init failed — agent will only respond to pings", "err", err)
	}
	var cs *compose.Service
	if dockerCli != nil {
		wrapped := dockerwrap.Wrap(dockerCli) //nolint:contextcheck // the monitor stops on Close, deferred below
		defer wrapped.Close()
		cs = compose.NewService(wrapped, nil)
	}
	system.SetRolePathsFn(agentRolePaths(dockerCli))

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			cancel()
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(serverSilence))
		var f agents.Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		switch f.Type {
		case agents.FrameServerPing:
			pong, _ := json.Marshal(agents.PingPayload{TS: time.Now().Unix()})
			if err := writeFrame(conn, agents.Frame{Type: agents.FrameAgentPong, Payload: pong}); err != nil {
				return err
			}
		case agents.FrameRes:
			deliverResponse(f)
		case agents.FrameReqCancel:
			cancelRequest(f.ID)
		case agents.FrameStreamOpen:
			var open agents.StreamOpen
			if err := json.Unmarshal(f.Payload, &open); err != nil {
				continue
			}
			// Registered before the goroutine starts so input that
			// follows the open frame finds the stream.
			reg := registerStream(ctxConn, open)
			go startStream(conn, dockerCli, open, reg) //nolint:contextcheck // reg.ctx derives from ctxConn

		case agents.FrameStreamData:
			// Server → agent data on an existing stream (exec stdin).
			var sd agents.StreamData
			if err := json.Unmarshal(f.Payload, &sd); err != nil {
				continue
			}
			deliverStreamInput(conn, sd.StreamID, sd.Data)

		case agents.FrameStreamControl:
			var sc agents.StreamControl
			if err := json.Unmarshal(f.Payload, &sc); err != nil {
				continue
			}
			deliverStreamControl(sc.StreamID, sc.Op, sc.Params)

		case agents.FrameStreamClose:
			var sc agents.StreamClose
			_ = json.Unmarshal(f.Payload, &sc)
			closeStream(sc.StreamID)

		default:
			// Anything else is a server request, handled in its own
			// goroutine so a slow handler can't block the read loop.
			reqCtx, reqCancel := context.WithCancel(ctxConn)
			trackRequest(f.ID, reqCancel)
			go func(f agents.Frame) {
				defer untrackRequest(f.ID)
				defer reqCancel()
				handleRequest(reqCtx, conn, dockerCli, cs, f)
			}(f)
		}
	}
}

// -----------------------------------------------------------------------------
// Stream registry — tracks running goroutines so stream.close can cancel them.
// -----------------------------------------------------------------------------

// streamReg tracks a single in-flight stream on the agent side. With
// flow control (the server sent a window) outbound data waits for credit
// and inbound data is acknowledged once written; without it, input that
// overflows the queue fails the stream.
type streamReg struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	credit *agents.Credit // nil without flow control

	input   chan []byte   // server → agent data (stdin, import)
	eof     chan struct{} // closed when the server ends its direction
	eofOnce sync.Once

	mu     sync.Mutex
	resize func(rows, cols uint) error
}

var (
	streamMu  sync.Mutex
	streamMap = map[string]*streamReg{}
)

func registerStream(parent context.Context, open agents.StreamOpen) *streamReg {
	ctx, cancel := context.WithCancel(parent)
	r := &streamReg{
		id:     open.StreamID,
		ctx:    ctx,
		cancel: cancel,
		input:  make(chan []byte, agents.StreamWindow),
		eof:    make(chan struct{}),
	}
	if n := agents.CreditCount(open.Params["window"]); n > 0 {
		r.credit = agents.NewCredit(n)
	}
	streamMu.Lock()
	streamMap[open.StreamID] = r
	streamMu.Unlock()
	return r
}

func lookupStream(id string) *streamReg {
	streamMu.Lock()
	defer streamMu.Unlock()
	return streamMap[id]
}

func deregisterStream(id string) {
	streamMu.Lock()
	delete(streamMap, id)
	streamMu.Unlock()
}

func closeStream(id string) {
	streamMu.Lock()
	r, ok := streamMap[id]
	if ok {
		delete(streamMap, id)
	}
	streamMu.Unlock()
	if ok {
		r.cancel()
	}
}

// deliverStreamInput queues server → agent data without blocking the
// read loop. A server that ignores the window overflows the queue, which
// fails the stream instead of dropping bytes.
func deliverStreamInput(conn *websocket.Conn, id string, data []byte) {
	r := lookupStream(id)
	if r == nil {
		return
	}
	select {
	case r.input <- data:
	default:
		closeStream(id)
		sendStreamClose(conn, id, "stream input overflow")
	}
}

// deliverStreamControl handles the resize, credit and eof control ops.
func deliverStreamControl(id, op string, params map[string]any) {
	r := lookupStream(id)
	if r == nil {
		return
	}
	switch op {
	case agents.StreamOpCredit:
		if r.credit != nil {
			r.credit.Add(agents.CreditCount(params["n"]))
		}
	case agents.StreamOpEOF:
		r.eofOnce.Do(func() { close(r.eof) })
	case "resize":
		r.mu.Lock()
		resize := r.resize
		r.mu.Unlock()
		if resize == nil {
			return
		}
		cols := uintFromAny(params["cols"])
		rows := uintFromAny(params["rows"])
		if cols > 0 && rows > 0 {
			_ = resize(rows, cols)
		}
	}
}

func uintFromAny(v any) uint {
	switch n := v.(type) {
	case float64:
		return uint(n)
	case int:
		return uint(n)
	case uint:
		return n
	}
	return 0
}

// sendData writes one stream.data frame, waiting for credit first when
// the stream is flow controlled.
func (r *streamReg) sendData(conn *websocket.Conn, data []byte) error {
	if r.credit != nil {
		if err := r.credit.Acquire(r.ctx, nil); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(agents.StreamData{StreamID: r.id, Data: data})
	return writeFrame(conn, agents.Frame{Type: agents.FrameStreamData, Payload: payload})
}

// pumpOutput copies src to the server as stream.data frames until EOF
// (nil) or an error.
func (r *streamReg) pumpOutput(conn *websocket.Conn, src io.Reader, bufSize int) error {
	buf := make([]byte, bufSize)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if err := r.sendData(conn, append([]byte(nil), buf[:n]...)); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// pumpInput writes queued server data to dst until the server sends eof
// (nil, after the queue is drained) or the stream ends (ctx error).
// Written frames are acknowledged in batches.
func (r *streamReg) pumpInput(conn *websocket.Conn, dst io.Writer) error {
	written := 0
	write := func(chunk []byte) error {
		if _, err := dst.Write(chunk); err != nil {
			return err
		}
		if r.credit == nil {
			return nil
		}
		if written++; written >= agents.GrantBatch {
			payload, _ := json.Marshal(agents.StreamControl{StreamID: r.id, Op: agents.StreamOpCredit, Params: map[string]any{"n": written}})
			written = 0
			return writeFrame(conn, agents.Frame{Type: agents.FrameStreamControl, Payload: payload})
		}
		return nil
	}
	for {
		select {
		case chunk := <-r.input:
			if err := write(chunk); err != nil {
				return err
			}
		case <-r.eof:
			for {
				select {
				case chunk := <-r.input:
					if err := write(chunk); err != nil {
						return err
					}
				default:
					return nil
				}
			}
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
}

// startStream dispatches to the right reader for the requested stream
// kind.
func startStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) {
	defer deregisterStream(open.StreamID)
	defer reg.cancel()
	if cli == nil {
		sendStreamClose(conn, open.StreamID, "docker daemon unavailable")
		return
	}
	var err error
	switch open.Kind {
	case "logs":
		err = runLogStream(conn, cli, open, reg)
	case "stats":
		err = runStatsStream(conn, cli, open, reg)
	case "exec":
		err = runExecStream(conn, cli, open, reg)
	case "volume_export":
		err = runVolumeExportStream(conn, cli, open, reg)
	case "volume_import":
		err = runVolumeImportStream(conn, cli, open, reg)
	case "events":
		err = runEventsStream(conn, cli, reg)
	default:
		err = errors.New("unknown stream kind: " + open.Kind)
	}
	if reg.ctx.Err() != nil {
		// The server closed the stream or the session ended.
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	sendStreamClose(conn, open.StreamID, msg)
}

// runEventsStream forwards Docker container events, one JSON object per
// chunk, for the server's event alerts.
func runEventsStream(conn *websocket.Conn, cli *client.Client, reg *streamReg) error {
	msgs, errs := cli.Events(reg.ctx, events.ListOptions{
		Filters: filters.NewArgs(filters.Arg("type", string(events.ContainerEventType))),
	})
	for {
		select {
		case <-reg.ctx.Done():
			return nil
		case err := <-errs:
			return err
		case m := <-msgs:
			data, err := json.Marshal(m)
			if err != nil {
				continue
			}
			if err := reg.sendData(conn, data); err != nil {
				return err
			}
		}
	}
}

func runStatsStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	resp, err := cli.ContainerStats(reg.ctx, open.Container, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return reg.pumpOutput(conn, resp.Body, 4096)
}

func runExecStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	ctx := reg.ctx
	cmdSlice := []string{"/bin/sh"}
	if raw, ok := open.Params["cmd"].([]any); ok {
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			cmdSlice = out
		}
	}

	createResp, err := cli.ContainerExecCreate(ctx, open.Container, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmdSlice,
	})
	if err != nil {
		return err
	}
	hijack, err := cli.ContainerExecAttach(ctx, createResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return err
	}
	defer hijack.Close()

	resize := func(rows, cols uint) error {
		return cli.ContainerExecResize(ctx, createResp.ID, container.ResizeOptions{Height: rows, Width: cols})
	}
	reg.mu.Lock()
	reg.resize = resize
	reg.mu.Unlock()
	if c, r := uintFromAny(open.Params["cols"]), uintFromAny(open.Params["rows"]); c > 0 && r > 0 {
		_ = resize(r, c)
	}

	go func() { _ = reg.pumpInput(conn, hijack.Conn) }()
	return reg.pumpOutput(conn, hijack.Reader, 4096)
}

func runLogStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	tail, _ := open.Params["tail"].(string)
	if tail == "" {
		tail = "100"
	}
	follow := true
	if v, ok := open.Params["follow"].(bool); ok {
		follow = v
	}
	rc, err := cli.ContainerLogs(reg.ctx, open.Container, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
	if err != nil {
		return err
	}
	defer rc.Close()
	return reg.pumpOutput(conn, rc, 8192)
}

func sendStreamClose(conn *websocket.Conn, id, errMsg string) {
	payload, _ := json.Marshal(agents.StreamClose{StreamID: id, Error: errMsg})
	_ = writeFrame(conn, agents.Frame{Type: agents.FrameStreamClose, Payload: payload})
}

const helperImage = "busybox:latest"

// helperTimeout bounds cleanup and exit-status calls that must still run
// after the stream's context is gone.
const helperTimeout = 60 * time.Second

func ensureHelperImage(ctx context.Context, cli *client.Client) error {
	if _, err := cli.ImageInspect(ctx, helperImage); err == nil {
		return nil
	}
	rc, err := cli.ImagePull(ctx, helperImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull busybox: %w", err)
	}
	_, _ = io.Copy(io.Discard, rc)
	return rc.Close()
}

// removeHelper deletes a helper container on a fresh context, so it is
// also removed when the stream was cancelled.
func removeHelper(cli *client.Client, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		slog.Warn("remove volume helper container", "id", id, "err", err)
	}
}

// helperExit waits for a helper container and turns a non-zero exit into
// an error.
func helperExit(cli *client.Client, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	statusCh, errCh := cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case s := <-statusCh:
		if s.StatusCode != 0 {
			return fmt.Errorf("tar exit %d", s.StatusCode)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// runVolumeExportStream creates a temporary busybox container that mounts
// the named volume read-only and runs `tar czf - -C /source .`, streaming
// the gzipped archive to the server via stream.data frames. tar's exit
// status decides whether the stream ends cleanly.
func runVolumeExportStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	ctx := reg.ctx
	volumeName, _ := open.Params["volume"].(string)
	if err := dockerwrap.CheckVolumeName(volumeName); err != nil {
		return err
	}
	if err := ensureHelperImage(ctx, cli); err != nil {
		return err
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        helperImage,
		Cmd:          []string{"sh", "-c", "tar czf - -C /source ."},
		AttachStdout: true,
		AttachStderr: true,
	}, &container.HostConfig{
		Binds:     []string{volumeName + ":/source:ro"},
		LogConfig: container.LogConfig{Type: "none"},
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}
	defer removeHelper(cli, resp.ID)

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer hijack.Close()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := reg.pumpOutput(conn, hijack.Reader, 32*1024); err != nil {
		return err
	}
	return helperExit(cli, resp.ID)
}

// runVolumeImportStream creates a temporary busybox container that mounts
// the named volume and pipes incoming stream.data frames into `tar xzf -`.
// Data that arrives before the helper runs waits in the stream's input
// queue. The server ends its data with an eof control; the agent then
// closes tar's stdin and reports tar's exit status in stream.close.
func runVolumeImportStream(conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) error {
	ctx := reg.ctx
	volumeName, _ := open.Params["volume"].(string)
	replace, _ := open.Params["replace"].(bool)
	if err := dockerwrap.CheckVolumeName(volumeName); err != nil {
		return err
	}
	if err := ensureHelperImage(ctx, cli); err != nil {
		return err
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:       helperImage,
		Cmd:         []string{"sh", "-c", agents.VolumeImportScript(replace)},
		AttachStdin: true,
		OpenStdin:   true,
		StdinOnce:   true,
	}, &container.HostConfig{
		Binds:     []string{volumeName + ":/dest"},
		LogConfig: container.LogConfig{Type: "none"},
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}
	defer removeHelper(cli, resp.ID)

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true, Stdin: true,
	})
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer hijack.Close()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := reg.pumpInput(conn, hijack.Conn); err != nil {
		return err
	}
	_ = hijack.CloseWrite()
	return helperExit(cli, resp.ID)
}

// -----------------------------------------------------------------------------
// Request handlers
// -----------------------------------------------------------------------------

func handleRequest(ctx context.Context, conn *websocket.Conn, cli *client.Client, cs *compose.Service, f agents.Frame) {
	if cli == nil {
		sendResponse(conn, f.ID, agents.ResponseEnvelope{OK: false, Error: "docker daemon unavailable"})
		return
	}
	switch f.Type {
	case agents.FrameReqContainerList:
		var req agents.ContainerListReq
		_ = json.Unmarshal(f.Payload, &req)
		list, err := cli.ContainerList(ctx, container.ListOptions{All: req.All})
		respond(conn, f.ID, list, err)

	case agents.FrameReqContainerInspect:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		info, err := cli.ContainerInspect(ctx, req.ID)
		respond(conn, f.ID, info, err)

	case agents.FrameReqContainerStart:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerStart(ctx, req.ID, container.StartOptions{})
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerStop:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerStop(ctx, req.ID, container.StopOptions{})
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerRestart:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerRestart(ctx, req.ID, container.StopOptions{})
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerRemove:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerRemove(ctx, req.ID, container.RemoveOptions{Force: req.Force})
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerExecRun:
		var req agents.ContainerExecRunReq
		_ = json.Unmarshal(f.Payload, &req)
		maxOut := req.MaxOutputBytes
		if maxOut <= 0 {
			maxOut = 1 << 20
		}
		out, exitCode, err := dockerwrap.RunExec(ctx, cli, req.Container, req.Cmd, maxOut)
		if err != nil {
			respond(conn, f.ID, nil, err)
			break
		}
		respond(conn, f.ID, agents.ContainerExecRunRes{Stdout: out, ExitCode: exitCode}, nil)

	case agents.FrameReqContainerPause:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerPause(ctx, req.ID)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerUnpause:
		var req agents.ContainerIDReq
		_ = json.Unmarshal(f.Payload, &req)
		err := cli.ContainerUnpause(ctx, req.ID)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqContainerKill:
		var req agents.ContainerKillReq
		_ = json.Unmarshal(f.Payload, &req)
		// Empty signal → Docker defaults to SIGKILL.
		err := cli.ContainerKill(ctx, req.ID, req.Signal)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqImageList:
		list, err := cli.ImageList(ctx, image.ListOptions{All: false})
		respond(conn, f.ID, list, err)

	case agents.FrameReqImageInspect:
		var req agents.ResourceIDReq
		_ = json.Unmarshal(f.Payload, &req)
		info, err := cli.ImageInspect(ctx, req.ID)
		respond(conn, f.ID, info, err)

	case agents.FrameReqImageRemove:
		var req agents.ImageRemoveReq
		_ = json.Unmarshal(f.Payload, &req)
		deleted, err := cli.ImageRemove(ctx, req.ID, image.RemoveOptions{Force: req.Force, PruneChildren: true})
		respond(conn, f.ID, deleted, err)

	case agents.FrameReqImagePrune:
		report, err := cli.ImagesPrune(ctx, filters.Args{})
		respond(conn, f.ID, report, err)

	case agents.FrameReqNetworkList:
		list, err := cli.NetworkList(ctx, network.ListOptions{})
		respond(conn, f.ID, list, err)

	case agents.FrameReqNetworkInspect:
		var req agents.ResourceIDReq
		_ = json.Unmarshal(f.Payload, &req)
		net, err := cli.NetworkInspect(ctx, req.ID, network.InspectOptions{})
		respond(conn, f.ID, net, err)

	case agents.FrameReqVolumeInspect:
		var req agents.ResourceIDReq
		_ = json.Unmarshal(f.Payload, &req)
		vol, err := cli.VolumeInspect(ctx, req.ID)
		respond(conn, f.ID, vol, err)

	case agents.FrameReqVolumeList:
		list, err := cli.VolumeList(ctx, volume.ListOptions{})
		if err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		// VolumeList returns a struct with pointer slice — flatten so the
		// server gets a JSON array directly.
		respond(conn, f.ID, list.Volumes, nil)

	case agents.FrameReqVolumeBrowse:
		// Path checks and the helper-container fallback are shared with the
		// server so the agent cannot drift from its containment rules.
		var req agents.VolumeBrowseReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := dockerwrap.CheckVolumeName(req.Volume); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		vol, err := cli.VolumeInspect(ctx, req.Volume)
		if err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		mp, err := host.ExtractMountpoint(vol.Mountpoint)
		if err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		entries, err := host.BrowseVolumeOnHost(ctx, cli, req.Volume, mp, req.SubPath)
		respond(conn, f.ID, entries, err)

	case agents.FrameReqVolumeBrowseFile:
		var req agents.VolumeBrowseReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := dockerwrap.CheckVolumeName(req.Volume); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		vol, err := cli.VolumeInspect(ctx, req.Volume)
		if err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		mp, err := host.ExtractMountpoint(vol.Mountpoint)
		if err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		res, err := host.ReadVolumeFileOnHost(ctx, cli, req.Volume, mp, req.SubPath, req.MaxBytes)
		respond(conn, f.ID, res, err)

	case agents.FrameReqDaemonInfo:
		info, err := cli.Info(ctx)
		respond(conn, f.ID, info, err)

	case agents.FrameReqSystemMetrics:
		// Read host-level CPU / memory / disk / uptime by calling the
		// shared system package on the agent's own host. Same code path
		// the central server uses for its local host, just executed on
		// the agent side via the protocol. Slice P.6.
		respond(conn, f.ID, system.Collect(), nil)

	case agents.FrameReqSystemDisks:
		respond(conn, f.ID, system.LocalMounts(ctx), nil)

	case agents.FrameReqStackDeploy:
		var req agents.StackDeployReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentDeployStack(ctx, cs, req)
		respond(conn, f.ID, res, err)

	case agents.FrameReqStackStop:
		var req agents.StackNameReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := stacks.ValidateName(req.Name); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		err := cs.Stop(ctx, req.Name)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqStackStatus:
		var req agents.StackNameReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := stacks.ValidateName(req.Name); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		out, err := cs.Status(ctx, req.Name)
		respond(conn, f.ID, out, err)

	case agents.FrameReqStackScale:
		var req agents.StackScaleReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentScaleService(ctx, cs, req)
		respond(conn, f.ID, res, err)

	case agents.FrameReqStackCheckScale:
		var req agents.StackCheckScaleReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentCheckScale(ctx, cs, req)
		respond(conn, f.ID, res, err)

	case agents.FrameReqAgentUpgrade:
		var req agents.AgentUpgradeReq
		_ = json.Unmarshal(f.Payload, &req)
		err := agentSelfUpgrade(req)
		respond(conn, f.ID, map[string]string{"status": "upgrading"}, err)
		if err == nil {
			// Give time for the response to be sent, then restart.
			go func() {
				time.Sleep(2 * time.Second)
				slog.Info("agent upgrade: restarting via systemd")
				_ = exec.Command("systemctl", "restart", "dockmesh-agent").Run()
				// If systemctl isn't available (dev), just exit and let the
				// supervisor restart us.
				os.Exit(0)
			}()
		}

	case agents.FrameReqVolumeTarExport:
		// Volume tar-export is handled as a stream, not a request/response,
		// because the tar data can be large. The agent starts a helper
		// container and pumps tar bytes via stream.data frames.
		// This frame starts the process; the actual streaming is handled
		// by the stream infrastructure (kind="volume_export").
		var req agents.VolumeTarReq
		_ = json.Unmarshal(f.Payload, &req)
		respond(conn, f.ID, map[string]string{"status": "use stream kind=volume_export"}, nil)

	case agents.FrameReqVolumeTarImport:
		var req agents.VolumeTarReq
		_ = json.Unmarshal(f.Payload, &req)
		respond(conn, f.ID, map[string]string{"status": "use stream kind=volume_import"}, nil)

	case agents.FrameReqStackSync:
		var req agents.StackSyncReq
		_ = json.Unmarshal(f.Payload, &req)
		err := syncStack(dataDir(), req)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqStackDelete:
		var req agents.StackNameReq
		_ = json.Unmarshal(f.Payload, &req)
		err := deleteStack(dataDir(), req.Name)
		respond(conn, f.ID, struct{}{}, err)

	default:
		sendResponse(conn, f.ID, agents.ResponseEnvelope{OK: false, Error: "unknown request type: " + f.Type})
	}
}

// agentDeployStack stages compose+env into a tmpdir, parses, and runs the
// shared executor against the local docker daemon.
func agentDeployStack(ctx context.Context, cs *compose.Service, req agents.StackDeployReq) (*compose.DeployResult, error) {
	if err := stacks.ValidateName(req.Name); err != nil {
		return nil, err
	}
	stagingBase := filepath.Join(dataDir(), "staging")
	if err := os.MkdirAll(stagingBase, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(stagingBase, req.Name+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(req.Compose), 0o600); err != nil {
		return nil, err
	}
	if req.Env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(req.Env), 0o600); err != nil {
			return nil, err
		}
	}

	proj, err := agentLoadStaged(ctx, dir, req.Name, req.Env)
	if err != nil {
		return nil, err
	}
	return cs.DeployProject(ctx, proj)
}

// agentLoadStaged parses the staged compose file and points relative bind
// mounts at the agent's persistent copy of the stack directory.
func agentLoadStaged(ctx context.Context, dir, name, env string) (*composetypes.Project, error) {
	if err := stacks.ValidateName(name); err != nil {
		return nil, err
	}
	proj, err := compose.LoadProject(ctx, dir, name, env)
	if err != nil {
		return nil, err
	}
	// Created through an os.Root on the stacks directory, so the name can
	// only ever create a directory inside it.
	root, err := openStacksRoot(dataDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.MkdirAll(name, 0o750); err != nil {
		return nil, err
	}
	compose.RebaseBindSources(proj, dir, filepath.Join(root.Name(), name))
	return proj, nil
}

func agentScaleService(ctx context.Context, cs *compose.Service, req agents.StackScaleReq) (*compose.ScaleResult, error) {
	if err := stacks.ValidateName(req.Name); err != nil {
		return nil, err
	}
	dir, cleanup, err := host.WriteStagingDir(req.Name, req.Compose, req.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := agentLoadStaged(ctx, dir, req.Name, req.Env)
	if err != nil {
		return nil, err
	}
	return cs.ScaleService(ctx, proj, req.Service, req.Replicas)
}

func agentCheckScale(ctx context.Context, cs *compose.Service, req agents.StackCheckScaleReq) (*compose.ScaleCheck, error) {
	if err := stacks.ValidateName(req.Name); err != nil {
		return nil, err
	}
	dir, cleanup, err := host.WriteStagingDir(req.Name, req.Compose, req.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	proj, err := agentLoadStaged(ctx, dir, req.Name, req.Env)
	if err != nil {
		return nil, err
	}
	return cs.CheckScale(ctx, proj, req.Service)
}

// agentRolePaths labels the filesystems holding Docker, containerd, the
// stacks dir and the agent's data dir in the disk report.
func agentRolePaths(cli *client.Client) system.RolePathsFn {
	return func(ctx context.Context) []system.RolePath {
		dir := dataDir()
		paths := []system.RolePath{
			{Role: system.RoleContainerdRoot, Path: system.ContainerdRoot()},
			{Role: system.RoleData, Path: dir},
			{Role: system.RoleStacks, Path: filepath.Join(dir, "stacks")},
		}
		if cli == nil {
			return paths
		}
		ictx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if info, err := cli.Info(ictx); err == nil && info.DockerRootDir != "" {
			paths = append(paths, system.RolePath{Role: system.RoleDockerRoot, Path: info.DockerRootDir})
		}
		return paths
	}
}

func respond(conn *websocket.Conn, id string, data any, err error) {
	if err != nil {
		sendResponse(conn, id, agents.ResponseEnvelope{OK: false, Error: err.Error()})
		return
	}
	b, mErr := json.Marshal(data)
	if mErr != nil {
		sendResponse(conn, id, agents.ResponseEnvelope{OK: false, Error: mErr.Error()})
		return
	}
	sendResponse(conn, id, agents.ResponseEnvelope{OK: true, Data: b})
}

func sendResponse(conn *websocket.Conn, id string, env agents.ResponseEnvelope) {
	envBytes, _ := json.Marshal(env)
	_ = writeFrame(conn, agents.Frame{Type: agents.FrameRes, ID: id, Payload: envBytes})
}

// -----------------------------------------------------------------------------
// TLS
// -----------------------------------------------------------------------------

func newCertStore(certPath, keyPath, caPath string) (*certStore, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca cert not parseable")
	}
	s := &certStore{certPath: certPath, keyPath: keyPath, roots: pool}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// -----------------------------------------------------------------------------
// Docker daemon helpers
// -----------------------------------------------------------------------------

func dockerDaemonVersion(ctx context.Context) string {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return ""
	}
	defer cli.Close()
	v, err := cli.ServerVersion(ctx)
	if err != nil {
		return ""
	}
	return v.Version
}

func dockerContainerCount(ctx context.Context) int32 {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return 0
	}
	defer cli.Close()
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return 0
	}
	return int32(len(list))
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeMu serialises every conn.WriteMessage on the single agent→server
// WebSocket. gorilla/websocket explicitly documents that Write methods
// are NOT safe for concurrent use, and the agent has several goroutines
// that write independently: the heartbeat timer, each in-flight request
// handler (stats, logs, deploys, system-metrics), plus ping/pong. A
// single shared mutex is the minimal fix — a per-connection write
// channel + writer goroutine would be cleaner but is overkill here.
// Without this, panics looked like:
//
//	panic: concurrent write to websocket connection
//	gorilla/websocket@v1.5.3/conn.go:520
//
// which tripped the agent into a systemd restart loop under even
// modest load (one stats stream + one heartbeat tick colliding).
var writeMu sync.Mutex

func writeFrame(conn *websocket.Conn, f agents.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, b)
}

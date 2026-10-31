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
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	dockerwrap "github.com/dockmesh/dockmesh/internal/docker"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
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

// agentVersion returns the build-stamped version so `make build`
// flags -X pkg/version.Version=… actually surface in the UI + in
// the upgrade decision. Previously this was a hardcoded const so
// every install showed "0.1.0-dev" regardless of the binary hash.
func agentVersion() string {
	v := version.Version
	if version.Commit != "" && version.Commit != "none" {
		return v + "+" + version.Commit
	}
	return v
}

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

	tlsCfg, err := loadTLS(certPath, keyPath, caPath)
	if err != nil {
		slog.Error("tls config", "err", err)
		os.Exit(1)
	}
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

	// Reconnect loop with exponential backoff capped at 60s.
	backoff := time.Second
	for ctx.Err() == nil {
		err := runOnce(ctx, dialURL, tlsCfg)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			slog.Warn("disconnected, reconnecting", "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

func runOnce(ctx context.Context, dialURL string, tlsCfg *tls.Config) error {
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

	// Send hello
	hostname, _ := os.Hostname()
	dockerVersion := dockerDaemonVersion(ctx)
	hello := agents.HelloPayload{
		Version:       version.Version,
		Commit:        version.Commit,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Hostname:      hostname,
		DockerVersion: dockerVersion,
	}
	helloBytes, _ := json.Marshal(hello)
	if err := writeFrame(conn, agents.Frame{Type: agents.FrameAgentHello, Payload: helloBytes}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	// Wait for welcome
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
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
	slog.Info("connected", "agent_id", welcome.AgentID, "agent_name", welcome.AgentName, "server", welcome.ServerVersion)

	conn.SetReadDeadline(time.Time{})

	// Heartbeat ticker
	ctxConn, cancel := context.WithCancel(ctx)
	defer cancel()
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
					return
				}
			}
		}
	}()

	// Shared docker client for request handlers (re-created on each
	// request would be wasteful; one per connection is fine).
	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		slog.Warn("docker client init failed — agent will only respond to pings", "err", err)
	}
	if dockerCli != nil {
		defer dockerCli.Close()
	}

	// Read loop: handle server pings + request/response frames.
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			cancel()
			return err
		}
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
		case agents.FrameStreamOpen:
			var open agents.StreamOpen
			if err := json.Unmarshal(f.Payload, &open); err != nil {
				continue
			}
			go startStream(ctxConn, conn, dockerCli, open)

		case agents.FrameStreamData:
			// Server → agent data on an existing stream (exec stdin).
			var sd agents.StreamData
			if err := json.Unmarshal(f.Payload, &sd); err != nil {
				continue
			}
			deliverStreamInput(sd.StreamID, sd.Data)

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
			// Anything starting with req. is a server-initiated request.
			// Handle it in a goroutine so a slow handler can't block the
			// read loop (and therefore the heartbeat too).
			go handleRequest(ctxConn, conn, dockerCli, f)
		}
	}
}

// -----------------------------------------------------------------------------
// Stream registry — tracks running goroutines so stream.close can cancel them.
// -----------------------------------------------------------------------------

// streamReg tracks a single in-flight stream on the agent side. Logs and
// stats only need cancel; exec also needs a writer (stdin) and a resize
// hook for control frames.
type streamReg struct {
	cancel context.CancelFunc
	stdin  io.Writer                   // nil for non-bidirectional streams
	resize func(rows, cols uint) error // nil if not resizable
}

var (
	streamMu  sync.Mutex
	streamMap = map[string]*streamReg{}
	streamWMu sync.Mutex // serialises writeFrame() so concurrent streams don't interleave WS frames
)

func registerStream(id string, cancel context.CancelFunc) *streamReg {
	r := &streamReg{cancel: cancel}
	streamMu.Lock()
	streamMap[id] = r
	streamMu.Unlock()
	return r
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

// deliverStreamInput is called when the server sends stream.data on an
// already-open stream — used to push exec stdin to the docker side.
func deliverStreamInput(id string, data []byte) {
	streamMu.Lock()
	r, ok := streamMap[id]
	streamMu.Unlock()
	if !ok || r.stdin == nil {
		return
	}
	_, _ = r.stdin.Write(data)
}

// deliverStreamControl handles out-of-band control ops (currently only
// exec resize).
func deliverStreamControl(id, op string, params map[string]any) {
	streamMu.Lock()
	r, ok := streamMap[id]
	streamMu.Unlock()
	if !ok {
		return
	}
	switch op {
	case "resize":
		if r.resize == nil {
			return
		}
		cols := uintFromAny(params["cols"])
		rows := uintFromAny(params["rows"])
		if cols > 0 && rows > 0 {
			_ = r.resize(rows, cols)
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

// safeWriteFrame is the agent's only WS write path. Holding the mutex
// guarantees that stream.data chunks from concurrent streams don't
// interleave inside the same WS message — gorilla/websocket panics if
// two goroutines try to write at once.
func safeWriteFrame(conn *websocket.Conn, f agents.Frame) error {
	streamWMu.Lock()
	defer streamWMu.Unlock()
	return writeFrame(conn, f)
}

// startStream dispatches to the right reader for the requested stream
// kind. New kinds (stats, exec) get added here.
func startStream(parent context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen) {
	if cli == nil {
		sendStreamClose(conn, open.StreamID, "docker daemon unavailable")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	reg := registerStream(open.StreamID, cancel)
	defer deregisterStream(open.StreamID)
	defer cancel()

	switch open.Kind {
	case "logs":
		runLogStream(ctx, conn, cli, open)
	case "stats":
		runStatsStream(ctx, conn, cli, open)
	case "exec":
		runExecStream(ctx, conn, cli, open, reg)
	case "volume_export":
		runVolumeExportStream(ctx, conn, cli, open)
	case "volume_import":
		runVolumeImportStream(ctx, conn, cli, open, reg)
	default:
		sendStreamClose(conn, open.StreamID, "unknown stream kind: "+open.Kind)
	}
}

func runStatsStream(ctx context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen) {
	resp, err := cli.ContainerStats(ctx, open.Container, true)
	if err != nil {
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			payload, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: append([]byte(nil), buf[:n]...)})
			if werr := safeWriteFrame(conn, agents.Frame{Type: agents.FrameStreamData, Payload: payload}); werr != nil {
				return
			}
		}
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				sendStreamClose(conn, open.StreamID, "")
			} else {
				sendStreamClose(conn, open.StreamID, rerr.Error())
			}
			return
		}
	}
}

func runExecStream(ctx context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) {
	// Decode params
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
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}
	hijack, err := cli.ContainerExecAttach(ctx, createResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}
	defer hijack.Close()

	// Wire up the registry hooks so deliverStreamInput / deliverStreamControl
	// can reach this exec instance.
	reg.stdin = hijack.Conn
	reg.resize = func(rows, cols uint) error {
		return cli.ContainerExecResize(ctx, createResp.ID, container.ResizeOptions{Height: rows, Width: cols})
	}

	// Apply the initial size if the client passed one in StreamOpen.Params.
	if c, r := uintFromAny(open.Params["cols"]), uintFromAny(open.Params["rows"]); c > 0 && r > 0 {
		_ = reg.resize(r, c)
	}

	// Pump exec stdout → server as stream.data frames.
	buf := make([]byte, 4096)
	for {
		n, rerr := hijack.Reader.Read(buf)
		if n > 0 {
			payload, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: append([]byte(nil), buf[:n]...)})
			if werr := safeWriteFrame(conn, agents.Frame{Type: agents.FrameStreamData, Payload: payload}); werr != nil {
				return
			}
		}
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				sendStreamClose(conn, open.StreamID, "")
			} else {
				sendStreamClose(conn, open.StreamID, rerr.Error())
			}
			return
		}
	}
}

func runLogStream(ctx context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen) {
	tail, _ := open.Params["tail"].(string)
	if tail == "" {
		tail = "100"
	}
	follow := true
	if v, ok := open.Params["follow"].(bool); ok {
		follow = v
	}
	rc, err := cli.ContainerLogs(ctx, open.Container, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: true,
	})
	if err != nil {
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}
	defer rc.Close()

	buf := make([]byte, 8192)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			payload, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: append([]byte(nil), buf[:n]...)})
			if werr := safeWriteFrame(conn, agents.Frame{Type: agents.FrameStreamData, Payload: payload}); werr != nil {
				return
			}
		}
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				sendStreamClose(conn, open.StreamID, "")
			} else {
				sendStreamClose(conn, open.StreamID, rerr.Error())
			}
			return
		}
	}
}

func sendStreamClose(conn *websocket.Conn, id, errMsg string) {
	payload, _ := json.Marshal(agents.StreamClose{StreamID: id, Error: errMsg})
	_ = safeWriteFrame(conn, agents.Frame{Type: agents.FrameStreamClose, Payload: payload})
}

// runVolumeExportStream creates a temporary busybox container that mounts
// the named volume read-only and runs `tar czf - -C /source .`, streaming
// the gzipped archive to the server via stream.data frames. Used by
// RemoteHost.VolumeTar for volume backups.
func runVolumeExportStream(ctx context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen) {
	volumeName, _ := open.Params["volume"].(string)
	if err := dockerwrap.CheckVolumeName(volumeName); err != nil {
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}

	const helperImage = "busybox:latest"
	// Ensure helper image exists.
	if _, err := cli.ImageInspect(ctx, helperImage); err != nil {
		rc, pullErr := cli.ImagePull(ctx, helperImage, image.PullOptions{})
		if pullErr != nil {
			sendStreamClose(conn, open.StreamID, "pull busybox: "+pullErr.Error())
			return
		}
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        helperImage,
		Cmd:          []string{"sh", "-c", "tar czf - -C /source ."},
		AttachStdout: true,
		AttachStderr: true,
	}, &container.HostConfig{
		Binds: []string{volumeName + ":/source:ro"},
	}, nil, nil, "")
	if err != nil {
		sendStreamClose(conn, open.StreamID, "create helper: "+err.Error())
		return
	}
	defer func() { _ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}) }()

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		sendStreamClose(conn, open.StreamID, "attach: "+err.Error())
		return
	}
	defer hijack.Close()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		sendStreamClose(conn, open.StreamID, "start: "+err.Error())
		return
	}

	buf := make([]byte, 32*1024)
	for {
		n, rerr := hijack.Reader.Read(buf)
		if n > 0 {
			payload, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: append([]byte(nil), buf[:n]...)})
			if werr := safeWriteFrame(conn, agents.Frame{Type: agents.FrameStreamData, Payload: payload}); werr != nil {
				return
			}
		}
		if rerr != nil {
			break
		}
	}
	sendStreamClose(conn, open.StreamID, "")
}

// runVolumeImportStream creates a temporary busybox container that mounts
// the named volume and pipes incoming stream.data frames into `tar xzf -`.
// Uses the existing stdin delivery mechanism (reg.stdin) so the server
// can push bytes via deliverStreamInput. When the server closes the
// stream, we close stdin and wait for tar to finish.
func runVolumeImportStream(ctx context.Context, conn *websocket.Conn, cli *client.Client, open agents.StreamOpen, reg *streamReg) {
	volumeName, _ := open.Params["volume"].(string)
	if err := dockerwrap.CheckVolumeName(volumeName); err != nil {
		sendStreamClose(conn, open.StreamID, err.Error())
		return
	}

	const helperImage = "busybox:latest"
	if _, err := cli.ImageInspect(ctx, helperImage); err != nil {
		rc, pullErr := cli.ImagePull(ctx, helperImage, image.PullOptions{})
		if pullErr != nil {
			sendStreamClose(conn, open.StreamID, "pull busybox: "+pullErr.Error())
			return
		}
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:       helperImage,
		Cmd:         []string{"sh", "-c", "tar xzf - -C /dest"},
		AttachStdin: true,
		OpenStdin:   true,
		StdinOnce:   true,
	}, &container.HostConfig{
		Binds: []string{volumeName + ":/dest"},
	}, nil, nil, "")
	if err != nil {
		sendStreamClose(conn, open.StreamID, "create helper: "+err.Error())
		return
	}
	defer func() { _ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}) }()

	hijack, err := cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true, Stdin: true,
	})
	if err != nil {
		sendStreamClose(conn, open.StreamID, "attach: "+err.Error())
		return
	}
	defer hijack.Close()

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		sendStreamClose(conn, open.StreamID, "start: "+err.Error())
		return
	}

	// Wire the stream registry's stdin to the hijacked connection so
	// deliverStreamInput() pushes bytes directly into the tar helper.
	reg.stdin = hijack.Conn

	// Wait for the container to exit (stdin will be closed when the
	// server sends stream.close, which triggers closeStream → cancel).
	<-ctx.Done()
	_ = hijack.CloseWrite()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer waitCancel()
	statusCh, errCh := cli.ContainerWait(waitCtx, resp.ID, container.WaitConditionNotRunning)
	select {
	case s := <-statusCh:
		if s.StatusCode != 0 {
			sendStreamClose(conn, open.StreamID, fmt.Sprintf("tar exit %d", s.StatusCode))
			return
		}
	case e := <-errCh:
		sendStreamClose(conn, open.StreamID, e.Error())
		return
	case <-waitCtx.Done():
		sendStreamClose(conn, open.StreamID, "tar timeout")
		return
	}
	sendStreamClose(conn, open.StreamID, "")
}

// -----------------------------------------------------------------------------
// Request handlers
// -----------------------------------------------------------------------------

func handleRequest(ctx context.Context, conn *websocket.Conn, cli *client.Client, f agents.Frame) {
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
		exec, err := cli.ContainerExecCreate(ctx, req.Container, container.ExecOptions{
			Cmd:          req.Cmd,
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			respond(conn, f.ID, nil, err)
			break
		}
		attach, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
		if err != nil {
			respond(conn, f.ID, nil, err)
			break
		}
		var out bytes.Buffer
		_, _ = io.CopyN(&out, attach.Reader, maxOut)
		attach.Close()
		insp, inspErr := cli.ContainerExecInspect(ctx, exec.ID)
		exitCode := -1
		if inspErr == nil {
			exitCode = insp.ExitCode
		}
		respond(conn, f.ID, agents.ContainerExecRunRes{Stdout: out.Bytes(), ExitCode: exitCode}, nil)

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

	case agents.FrameReqStackDeploy:
		var req agents.StackDeployReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentDeployStack(ctx, cli, req)
		respond(conn, f.ID, res, err)

	case agents.FrameReqStackStop:
		var req agents.StackNameReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := stacks.ValidateName(req.Name); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		err := compose.NewService(dockerwrap.Wrap(cli), nil).Stop(ctx, req.Name)
		respond(conn, f.ID, struct{}{}, err)

	case agents.FrameReqStackStatus:
		var req agents.StackNameReq
		_ = json.Unmarshal(f.Payload, &req)
		if err := stacks.ValidateName(req.Name); err != nil {
			respond(conn, f.ID, nil, err)
			return
		}
		out, err := compose.NewService(dockerwrap.Wrap(cli), nil).Status(ctx, req.Name)
		respond(conn, f.ID, out, err)

	case agents.FrameReqStackScale:
		var req agents.StackScaleReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentScaleService(ctx, cli, req)
		respond(conn, f.ID, res, err)

	case agents.FrameReqStackCheckScale:
		var req agents.StackCheckScaleReq
		_ = json.Unmarshal(f.Payload, &req)
		res, err := agentCheckScale(ctx, cli, req)
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
func agentDeployStack(ctx context.Context, cli *client.Client, req agents.StackDeployReq) (*compose.DeployResult, error) {
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
	return compose.NewService(dockerwrap.Wrap(cli), nil).DeployProject(ctx, proj)
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

func agentScaleService(ctx context.Context, cli *client.Client, req agents.StackScaleReq) (*compose.ScaleResult, error) {
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
	return compose.NewService(dockerwrap.Wrap(cli), nil).ScaleService(ctx, proj, req.Service, req.Replicas)
}

func agentCheckScale(ctx context.Context, cli *client.Client, req agents.StackCheckScaleReq) (*compose.ScaleCheck, error) {
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
	return compose.NewService(dockerwrap.Wrap(cli), nil).CheckScale(ctx, proj, req.Service)
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

func loadTLS(certPath, keyPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ca cert not parseable")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
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

// dockmesh-agent is the remote-host companion to the central dockmesh
// server. It connects outbound via mTLS and tells the server about the
// local docker daemon. Concept §3.1.
//
// Usage (typical):
//
//	docker run -d --name dockmesh-agent --restart unless-stopped \
//	  -v /var/run/docker.sock:/var/run/docker.sock \
//	  -v dockmesh-agent:/var/lib/dockmesh \
//	  -e DOCKMESH_ENROLL_URL=https://main:8080/api/v1/agents/enroll \
//	  -e DOCKMESH_AGENT_URL=wss://main:8443/connect \
//	  -e DOCKMESH_TOKEN=<token-from-admin-ui> \
//	  ghcr.io/dockmesh/agent:latest
//
// On first start the agent calls the enroll URL with its token, persists
// the returned cert + key under DATA_DIR, and then dials the agent URL
// over mTLS. On reboot it skips the enroll and goes straight to dial.
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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"
)

const agentVersion = "0.1.0-dev"

type agentConfig struct {
	dataDir   string
	enrollURL string
	agentURL  string
	token     string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := agentConfig{
		dataDir:   envOr("DOCKMESH_DATA_DIR", "/var/lib/dockmesh"),
		enrollURL: os.Getenv("DOCKMESH_ENROLL_URL"),
		agentURL:  os.Getenv("DOCKMESH_AGENT_URL"),
		token:     os.Getenv("DOCKMESH_TOKEN"),
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		slog.Info("shutting down")
		cancel()
	}()

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
		Version:       agentVersion,
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

	// Read loop: handle server pings + future request frames.
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
		}
	}
}

// -----------------------------------------------------------------------------
// Enrollment
// -----------------------------------------------------------------------------

func enroll(cfg agentConfig, certPath, keyPath, caPath, urlPath string) error {
	hostname, _ := os.Hostname()
	body := agents.EnrollRequest{
		Token:         cfg.token,
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Version:       agentVersion,
		DockerVersion: dockerDaemonVersion(context.Background()),
	}
	buf, _ := json.Marshal(body)

	// During enrollment we trust the server's TLS chain whatever it is —
	// either it's a public-CA-signed cert (HTTPS BaseURL) or the user has
	// pointed us at a private CA. In dev we accept self-signed with
	// InsecureSkipVerify because the BaseURL is typically http://, which
	// means the request goes plain anyway. Once enrolled, the agent
	// connection itself is mTLS pinned to OUR CA.
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	req, err := http.NewRequest(http.MethodPost, cfg.enrollURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enroll http %d: %s", resp.StatusCode, string(b))
	}
	var er agents.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return err
	}
	if er.ClientCert == "" || er.ClientKey == "" || er.CACert == "" {
		return errors.New("server returned empty cert payload")
	}
	if err := os.WriteFile(certPath, []byte(er.ClientCert), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(er.ClientKey), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(caPath, []byte(er.CACert), 0o644); err != nil {
		return err
	}
	if er.AgentURL != "" {
		_ = os.WriteFile(urlPath, []byte(er.AgentURL), 0o644)
	}
	slog.Info("cert persisted", "agent_id", er.AgentID, "cn", er.AgentName)
	return nil
}

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

func writeFrame(conn *websocket.Conn, f agents.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, b)
}

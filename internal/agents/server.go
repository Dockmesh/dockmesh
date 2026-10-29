package agents

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/pkg/version"
	"github.com/gorilla/websocket"
)

// WSHandler is the http.Handler for the mTLS-only agent listener. It
// upgrades to WebSocket, looks up the agent by its presented client cert
// fingerprint, and pumps frames in both directions.
type WSHandler struct {
	svc *Service
	pki *pki.Manager
}

func NewWSHandler(svc *Service, p *pki.Manager) *WSHandler {
	return &WSHandler{svc: svc, pki: p}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

const (
	// maxFrameBytes bounds one inbound frame; the largest legitimate
	// ones are 1 MiB file reads and exec output, base64-encoded.
	maxFrameBytes  = 16 << 20
	heartbeatGrace = 60 * time.Second
	pingInterval   = 30 * time.Second
	writeWait      = 10 * time.Second
)

func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// mTLS check: there must be exactly one peer certificate. The TLS
	// handler already validated it against our CA; we just look it up.
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	peer := r.TLS.PeerCertificates[0]
	fp := pki.FingerprintFromCert(peer)

	agent, err := h.svc.LookupByFingerprint(r.Context(), fp)
	if err != nil {
		slog.Warn("agent connect: unknown fingerprint", "fp", fp[:12], "err", err)
		http.Error(w, "agent not registered", http.StatusUnauthorized)
		return
	}
	if agent.Status == "revoked" {
		http.Error(w, "agent revoked", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("agent ws upgrade", "err", err)
		return
	}
	defer conn.Close()

	ag := newConnectedAgent(agent.ID, agent.Name, func() { _ = conn.Close() })
	ag.certNotAfter = peer.NotAfter
	conn.SetReadLimit(maxFrameBytes)

	// Wait for the hello frame before marking the agent online so the DB
	// reflects the most up-to-date hostinfo.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		slog.Warn("agent hello read", "agent", agent.Name, "err", err)
		return
	}
	var helloFrame Frame
	if err := json.Unmarshal(raw, &helloFrame); err != nil || helloFrame.Type != FrameAgentHello {
		slog.Warn("agent hello bad", "agent", agent.Name)
		return
	}
	var hello HelloPayload
	if err := json.Unmarshal(helloFrame.Payload, &hello); err != nil {
		slog.Warn("agent hello payload", "agent", agent.Name, "err", err)
		return
	}
	// The upgrade controller compares the reported commit, and stream /
	// cancel features depend on the advertised capabilities.
	ag.Hello = hello

	if err := h.svc.markOnline(r.Context(), ag, hello); err != nil {
		slog.Warn("agent markOnline", "agent", agent.Name, "err", err)
		// Send a small error frame and close.
		_ = conn.WriteMessage(websocket.TextMessage, errorFrame(err.Error()))
		return
	}
	defer h.svc.markOffline(ag)
	defer ag.closeAllStreams()
	defer ag.shutdown()

	if err := h.svc.recordCert(r.Context(), agent.ID, fp, peer.NotAfter); err != nil {
		slog.Warn("agent cert record", "agent", agent.Name, "err", err)
	}

	slog.Info("agent connected", "id", agent.ID, "name", agent.Name, "host", hello.Hostname,
		"version", hello.Version, "protocol", hello.ProtocolVersion)

	welcome, _ := json.Marshal(WelcomePayload{
		AgentID:         agent.ID,
		AgentName:       agent.Name,
		ServerVersion:   ServerVersion(),
		ProtocolVersion: ProtocolVersion,
		Capabilities:    serverCapabilities,
	})
	if err := writeFrame(conn, Frame{Type: FrameServerWelcome, Payload: welcome}); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(heartbeatGrace))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go h.pump(ag, conn)

	// Inbound loop (agent → server). Any frame proves the agent is alive.
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Info("agent disconnected", "name", agent.Name, "err", err)
			}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		switch f.Type {
		case FrameAgentHeartbeat:
			h.svc.touchHeartbeat(ctx, ag.ID)
		case FrameRes:
			ag.deliverResponse(f)
		case FrameStreamData, FrameStreamClose, FrameStreamControl:
			ag.routeStreamFrame(f)
		case FrameAgentCertRenew:
			go h.svc.handleCertRenew(ctx, ag, f)
		}
	}
}

// pump is the only writer on conn after the welcome: it drains the send
// queue and pings. A failed write ends the whole session, so the read
// loop can't outlive a dead writer.
func (h *WSHandler) pump(ag *ConnectedAgent, conn *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ag.done:
			return
		case f := <-ag.send:
			if err := writeFrame(conn, f); err != nil {
				ag.shutdown()
				return
			}
		case <-ticker.C:
			ping, _ := json.Marshal(PingPayload{TS: time.Now().Unix()})
			if err := writeFrame(conn, Frame{Type: FrameServerPing, Payload: ping}); err != nil {
				ag.shutdown()
				return
			}
		}
	}
}

// serverCapabilities is what this server offers agents in the welcome.
var serverCapabilities = []string{CapStreamCredit, CapCertRenew}

// ServerVersion is the build label agents see in the welcome.
func ServerVersion() string {
	if version.Commit != "" && version.Commit != "none" {
		return version.Version + "+" + version.Commit
	}
	return version.Version
}

func writeFrame(conn *websocket.Conn, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(websocket.TextMessage, b)
}

func errorFrame(msg string) []byte {
	b, _ := json.Marshal(Frame{Type: "server.error", Payload: json.RawMessage(`"` + msg + `"`)})
	return b
}

// ServerTLSConfig builds the *tls.Config for the agent listener. Client
// certs are verified against the agents CA whenever one is presented but
// are not required at the TLS layer, because enrollment happens before
// the agent has a cert. ServeHTTP rejects /connect without a verified
// client cert.
func ServerTLSConfig(p *pki.Manager) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(p.ServerCertPEM(), p.ServerKeyPEM())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    p.CACertPool(),
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

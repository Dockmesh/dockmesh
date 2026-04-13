package agents

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/pki"
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

	ag := &ConnectedAgent{
		ID:       agent.ID,
		Name:     agent.Name,
		JoinedAt: time.Now(),
		send:     make(chan Frame, 32),
	}

	// Wait for the hello frame before marking the agent online so the DB
	// reflects the most up-to-date hostinfo.
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
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

	if err := h.svc.markOnline(r.Context(), ag, hello); err != nil {
		slog.Warn("agent markOnline", "agent", agent.Name, "err", err)
		// Send a small error frame and close.
		_ = conn.WriteMessage(websocket.TextMessage, errorFrame(err.Error()))
		return
	}
	defer h.svc.markOffline(ag)

	slog.Info("agent connected", "id", agent.ID, "name", agent.Name, "host", hello.Hostname, "version", hello.Version)

	// Welcome
	welcome, _ := json.Marshal(WelcomePayload{
		AgentID:       agent.ID,
		AgentName:     agent.Name,
		ServerVersion: "dev",
	})
	if err := writeFrame(conn, Frame{Type: FrameServerWelcome, Payload: welcome}); err != nil {
		return
	}
	conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
		return nil
	})

	// Outbound pump (server → agent): drains ag.send + sends periodic pings.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-ag.send:
				if !ok {
					return
				}
				if err := writeFrame(conn, f); err != nil {
					return
				}
			case <-ticker.C:
				ping, _ := json.Marshal(PingPayload{TS: time.Now().Unix()})
				if err := writeFrame(conn, Frame{Type: FrameServerPing, Payload: ping}); err != nil {
					return
				}
			}
		}
	}()

	// Inbound loop (agent → server)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Info("agent disconnected", "name", agent.Name, "err", err)
			}
			return
		}
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		switch f.Type {
		case FrameAgentHeartbeat:
			h.svc.touchHeartbeat(ctx, ag.ID)
			conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
		case FrameAgentPong:
			conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
		case FrameRes:
			ag.deliverResponse(f)
			conn.SetReadDeadline(time.Now().Add(heartbeatGrace))
		}
	}
}

func writeFrame(conn *websocket.Conn, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(websocket.TextMessage, b)
}

func errorFrame(msg string) []byte {
	b, _ := json.Marshal(Frame{Type: "server.error", Payload: json.RawMessage(`"` + msg + `"`)})
	return b
}

// ServerTLSConfig builds the *tls.Config for the mTLS listener.
func ServerTLSConfig(p *pki.Manager) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(p.ServerCertPEM(), p.ServerKeyPEM())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    p.CACertPool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// silence unused import lint when the file is in flux
var _ = errors.New

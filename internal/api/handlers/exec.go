package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// WSExec proxies an interactive exec session over a WebSocket. Honours
// ?host=<id> for remote agents.
//
// Protocol (unchanged for both local and remote):
//   - BinaryMessage from client → stdin bytes to container
//   - BinaryMessage to client   ← stdout/stderr bytes (TTY merged)
//   - TextMessage (JSON)        ← resize control: {"type":"resize","cols":N,"rows":M}
//
// Auth via ?ticket= (§15.8). Optional ?cmd=/bin/bash, defaults to /bin/sh.
func (h *Handlers) WSExec(w http.ResponseWriter, r *http.Request) {
	r, ok := h.wsAuth(w, r, rbac.PermContainersExec)
	if !ok {
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	containerID := chi.URLParam(r, "id")
	if !h.wsContainerAccess(w, r, rbac.PermContainersExec, target.ID(), containerID) {
		return
	}
	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		cmd = "/bin/sh"
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws exec upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	details := map[string]any{"host_id": target.ID(), "cmd": cmd}
	if tok := middleware.APITokenID(r.Context()); tok != 0 {
		details["api_token_id"] = tok
	}
	session, err := target.StartExec(ctx, containerID, []string{cmd})
	if err != nil {
		details["error"] = err.Error()
		h.auditDetached(r, audit.ActionContainerExecStart, containerID, details)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
		return
	}
	defer session.Close()
	h.auditDetached(r, audit.ActionContainerExecStart, containerID, details)
	started := time.Now()
	defer func() {
		end := map[string]any{"host_id": target.ID(), "duration_s": int(time.Since(started).Seconds())}
		if tok, ok := details["api_token_id"]; ok {
			end["api_token_id"] = tok
		}
		h.auditDetached(r, audit.ActionContainerExecEnd, containerID, end)
	}()

	// Goroutine 1: container → WebSocket (binary frames).
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					cancel()
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					slog.Debug("exec read", "err", err)
				}
				cancel()
				return
			}
		}
	}()

	// Goroutine 2 (main): WebSocket → container + control messages.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			if _, err := session.Write(data); err != nil {
				return
			}
		case websocket.TextMessage:
			var ctrl struct {
				Type string `json:"type"`
				Cols uint   `json:"cols"`
				Rows uint   `json:"rows"`
			}
			if err := json.Unmarshal(data, &ctrl); err != nil {
				continue
			}
			if ctrl.Type == "resize" && ctrl.Cols > 0 && ctrl.Rows > 0 {
				_ = session.Resize(ctrl.Rows, ctrl.Cols)
			}
		}
	}
}

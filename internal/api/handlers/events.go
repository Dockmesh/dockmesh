package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
)

// WSEvents streams Docker events AND stack filesystem events as JSON messages
// over a WebSocket. Auth via ?ticket= query parameter (§15.8).
//
// Message shape:
//   Docker:  {"source":"docker", "type":"container", "action":"start", "id":"...", "name":"..."}
//   Stacks:  {"source":"stacks", "type":"modified|created|removed", "name":"...", "file":"compose.yaml"}
func (h *Handlers) WSEvents(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "ticket required", http.StatusUnauthorized)
		return
	}
	if _, err := h.Auth.ValidateWSTicket(ticket); err != nil {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws events upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Cancel ctx when the client disconnects.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	// Stack filesystem events (always available).
	stackCh, stackUnsub := h.Stacks.Subscribe()
	defer stackUnsub()

	// Docker events (optional — only if docker is reachable).
	var dockerMsgs <-chan dockerEventLike
	var dockerErrs <-chan error
	if h.Docker != nil {
		msgCh, errCh := h.Docker.Events(ctx)
		dockerErrs = errCh
		adapted := make(chan dockerEventLike, 8)
		go func() {
			defer close(adapted)
			for ev := range msgCh {
				adapted <- dockerEventLike{
					Source: "docker",
					Type:   string(ev.Type),
					Action: string(ev.Action),
					ID:     ev.Actor.ID,
					Name:   ev.Actor.Attributes["name"],
					Image:  ev.Actor.Attributes["image"],
					Time:   ev.Time,
				}
			}
		}()
		dockerMsgs = adapted
	}

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-dockerErrs:
			if err != nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
			}
			return
		case ev, ok := <-dockerMsgs:
			if !ok {
				dockerMsgs = nil
				continue
			}
			if b, err := json.Marshal(ev); err == nil {
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			}
		case ev, ok := <-stackCh:
			if !ok {
				stackCh = nil
				continue
			}
			payload := map[string]any{
				"source": "stacks",
				"type":   ev.Type,
				"name":   ev.Name,
				"file":   ev.File,
			}
			if b, err := json.Marshal(payload); err == nil {
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			}
		}
	}
}

type dockerEventLike struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Image  string `json:"image,omitempty"`
	Time   int64  `json:"time,omitempty"`
}

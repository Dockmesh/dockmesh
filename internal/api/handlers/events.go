package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
)

// WSEvents streams Docker events as JSON messages over a WebSocket.
// Auth via ?ticket= query parameter (same pattern as WSLogs, §15.8).
func (h *Handlers) WSEvents(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		http.Error(w, "docker unavailable", http.StatusServiceUnavailable)
		return
	}
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

	msgCh, errCh := h.Docker.Events(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
			}
			return
		case ev, ok := <-msgCh:
			if !ok {
				return
			}
			// Only forward the fields the UI cares about — full Docker events
			// are chatty and contain attributes not relevant for the list view.
			payload := map[string]any{
				"type":   string(ev.Type),
				"action": string(ev.Action),
				"id":     ev.Actor.ID,
				"name":   ev.Actor.Attributes["name"],
				"image":  ev.Actor.Attributes["image"],
				"time":   ev.Time,
			}
			b, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		}
	}
}

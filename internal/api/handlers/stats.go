package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// WSStats streams normalized container stats over a WebSocket.
// Auth via ?ticket= query parameter (§15.8).
func (h *Handlers) WSStats(w http.ResponseWriter, r *http.Request) {
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

	containerID := chi.URLParam(r, "id")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws stats upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Cancel ctx when client disconnects.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	statsCh, errCh := h.Docker.StreamStats(ctx, containerID)
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
			}
			return
		case s, ok := <-statsCh:
			if !ok {
				return
			}
			b, err := json.Marshal(s)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		}
	}
}

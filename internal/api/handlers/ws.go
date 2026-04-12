package handlers

import (
	"bufio"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// WSTicket issues a short-lived ticket for WebSocket auth (§15.8).
// Client POSTs here with a Bearer token, receives a 30s ticket to use
// as ?ticket= on the WS URL.
func (h *Handlers) WSTicket(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	role := middleware.Role(r.Context())
	ticket, err := h.Auth.IssueWSTicket(uid, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ticket generation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}

// WSLogs streams container logs over a WebSocket connection.
// Auth via ?ticket= query parameter (short-lived JWT from WSTicket).
func (h *Handlers) WSLogs(w http.ResponseWriter, r *http.Request) {
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
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "100"
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// Discard incoming messages (client doesn't send meaningful data).
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()

	rc, err := h.Docker.ContainerLogs(r.Context(), containerID, tail, true)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("error: "+err.Error()))
		return
	}
	defer rc.Close()

	// Docker multiplexes stdout/stderr with an 8-byte header per frame.
	// We scan the raw stream line by line and strip the mux header.
	// timestamps have one line per log entry after the 8-byte mux header.
	// We strip the header manually for tty containers or use stdcopy.
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Docker mux header is 8 bytes for non-tty containers.
		// If line starts with \x01 or \x02 (stdout/stderr marker),
		// strip the 8-byte header.
		if len(line) > 8 && (line[0] == 1 || line[0] == 2) {
			line = line[8:]
		}
		if err := conn.WriteMessage(websocket.TextMessage, line); err != nil {
			break
		}
	}
}

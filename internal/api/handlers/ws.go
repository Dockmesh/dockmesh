package handlers

import (
	"bufio"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// allowedWSTicketPerms gates which permissions a client can request a
// ticket for. The set matches the WS endpoints that exist today.
var allowedWSTicketPerms = map[rbac.Perm]bool{
	rbac.PermContainersView: true, // /ws/stats/{id}
	rbac.PermContainersLogs: true, // /ws/logs/{id}
	rbac.PermContainersExec: true, // /ws/exec/{id}
	rbac.PermSystemView:     true, // /ws/events
}

// WSTicket issues a short-lived ticket for WebSocket auth (§15.8).
// Client POSTs here with a Bearer token and a ?for=<perm> query (e.g.
// containers.exec). Server verifies the caller's role has that perm,
// then issues a 30s JWT bound to it. WS endpoints later compare the
// ticket's Perm against their required perm and reject on mismatch.
func (h *Handlers) WSTicket(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	role := middleware.Role(r.Context())
	// API-token auth produces no user id but does carry a role. dmctl
	// needs WS tickets for logs/exec. Fall back to a synthetic subject
	// derived from the token id so the ticket JWT stays unique + the
	// server knows it's a token-backed session.
	if uid == "" {
		if tokID := middleware.APITokenID(r.Context()); tokID != 0 {
			uid = "api-token:" + strconv.FormatInt(tokID, 10)
		}
	}
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	requested := rbac.Perm(r.URL.Query().Get("for"))
	if requested == "" {
		writeError(w, http.StatusBadRequest, "?for=<perm> required (one of containers.view, containers.logs, containers.exec, system.view)")
		return
	}
	if !allowedWSTicketPerms[requested] {
		writeError(w, http.StatusBadRequest, "permission "+string(requested)+" is not a valid WS ticket scope")
		return
	}

	// Verify the caller actually has the requested permission before
	// minting a ticket for it. Mirrors RequirePerm middleware: prefer
	// DB-backed store (custom roles) and fall back to built-ins.
	allowed := false
	if middleware.RBACStore != nil {
		allowed = middleware.RBACStore.AllowedDB(role, requested)
	} else {
		allowed = rbac.Allowed(role, requested)
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "your role is not allowed to "+string(requested))
		return
	}

	ticket, err := h.Auth.IssueWSTicket(uid, role, string(requested))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ticket generation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}

// requireWSTicketPerm validates the query-param ticket and asserts its
// embedded permission matches the endpoint's required permission. Writes
// the appropriate 401/403 and returns false on failure.
func (h *Handlers) requireWSTicketPerm(w http.ResponseWriter, r *http.Request, want rbac.Perm) bool {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "ticket required", http.StatusUnauthorized)
		return false
	}
	claims, err := h.Auth.ValidateWSTicket(ticket)
	if err != nil {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return false
	}
	if rbac.Perm(claims.Perm) != want {
		http.Error(w, "ticket not scoped for "+string(want), http.StatusForbidden)
		return false
	}
	return true
}

// WSLogs streams container logs over a WebSocket connection.
// Auth via ?ticket= query parameter (short-lived JWT from WSTicket).
// Reads ?host=<id> to pick a remote agent — falls back to local docker.
func (h *Handlers) WSLogs(w http.ResponseWriter, r *http.Request) {
	if !h.requireWSTicketPerm(w, r, rbac.PermContainersLogs) {
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	containerID := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), containerID)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersLogs, scopeReq, "container "+containerID)
		return
	}
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

	rc, err := target.ContainerLogs(r.Context(), containerID, tail, true)
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

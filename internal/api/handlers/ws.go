package handlers

import (
	"bufio"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
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

// WSTicket issues a short-lived, single-use ticket for WebSocket auth.
// Client POSTs here with a Bearer token and a ?for=<perm> query (e.g.
// containers.exec). The ticket carries the caller's full identity (user,
// role, scope, API token and its owner's role) so the WS handler runs the
// same permission and scope checks as a REST request. Clients mint a new
// ticket for every connect and reconnect.
func (h *Handlers) WSTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := middleware.UserID(ctx)
	tokID := middleware.APITokenID(ctx)
	if uid == "" && tokID == 0 {
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
	if !middleware.Allowed(ctx, requested) {
		writeError(w, http.StatusForbidden, "your role is not allowed to "+string(requested))
		return
	}

	ticket, err := h.Auth.IssueWSTicket(auth.WSTicket{
		UserID:     uid,
		Role:       middleware.Role(ctx),
		ScopeTags:  middleware.ScopeTags(ctx),
		Perm:       string(requested),
		APITokenID: tokID,
		OwnerRole:  middleware.TokenOwnerRole(ctx),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ticket generation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}

// wsAuth consumes the ?ticket= query parameter, checks that it was minted
// for want and that the caller still holds want, and returns r with the
// ticket's identity in its context so the usual user- and role-scope
// checks apply. Writes 401/403 and returns ok=false on failure.
func (h *Handlers) wsAuth(w http.ResponseWriter, r *http.Request, want rbac.Perm) (*http.Request, bool) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "ticket required", http.StatusUnauthorized)
		return r, false
	}
	t, err := h.Auth.ConsumeWSTicket(ticket)
	if err != nil {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return r, false
	}
	if rbac.Perm(t.Perm) != want {
		http.Error(w, "ticket not scoped for "+string(want), http.StatusForbidden)
		return r, false
	}
	ctx := middleware.WithIdentity(r.Context(), middleware.Identity{
		UserID:         t.UserID,
		Role:           t.Role,
		ScopeTags:      t.ScopeTags,
		APITokenID:     t.APITokenID,
		TokenOwnerRole: t.OwnerRole,
	})
	if !middleware.Allowed(ctx, want) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return r, false
	}
	return r.WithContext(ctx), true
}

// wsContainerAccess applies the host scope of the caller and the role scope
// for the container. Writes 403 and returns false when out of scope.
func (h *Handlers) wsContainerAccess(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) bool {
	if !h.canAccessHost(r, hostID) {
		http.Error(w, "out of scope: your role does not include this host", http.StatusForbidden)
		return false
	}
	scopeReq := h.containerScopeReq(r.Context(), hostID, containerID)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "container "+containerID)
		return false
	}
	return true
}

// WSLogs streams container logs over a WebSocket connection.
// Auth via ?ticket= query parameter (short-lived JWT from WSTicket).
// Reads ?host=<id> to pick a remote agent — falls back to local docker.
func (h *Handlers) WSLogs(w http.ResponseWriter, r *http.Request) {
	r, ok := h.wsAuth(w, r, rbac.PermContainersLogs)
	if !ok {
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	containerID := chi.URLParam(r, "id")
	if !h.wsContainerAccess(w, r, rbac.PermContainersLogs, target.ID(), containerID) {
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

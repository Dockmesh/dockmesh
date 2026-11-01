package handlers

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

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
		SessionID:  middleware.SessionID(ctx),
		APITokenID: tokID,
		Perm:       string(requested),
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
	id, err := h.wsIdentity(r.Context(), t)
	if err != nil {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return r, false
	}
	ctx := context.WithValue(middleware.WithIdentity(r.Context(), id), wsTicketKey{}, t)
	if !middleware.Allowed(ctx, want) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return r, false
	}
	return r.WithContext(middleware.WithRoutePerm(ctx, want)), true
}

type wsTicketKey struct{}

var errWSAccessGone = errors.New("session or token no longer valid")

// wsIdentity derives the caller from the ticket's session or API token as
// it is now, so a revoked session, a revoked token or a role change is
// seen even though the ticket itself is still valid.
func (h *Handlers) wsIdentity(ctx context.Context, t *auth.WSTicket) (middleware.Identity, error) {
	if t.APITokenID != 0 {
		if h.APITokens == nil {
			return middleware.Identity{}, errWSAccessGone
		}
		tok, err := h.APITokens.Get(ctx, t.APITokenID)
		if err != nil {
			return middleware.Identity{}, err
		}
		if tok.RevokedAt != nil || (tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now())) {
			return middleware.Identity{}, errWSAccessGone
		}
		id, ok := middleware.TokenIdentity(ctx, h.Auth, tok)
		if !ok {
			return middleware.Identity{}, errWSAccessGone
		}
		return id, nil
	}
	if err := h.Auth.SessionLive(ctx, t.UserID, t.SessionID); err != nil {
		return middleware.Identity{}, err
	}
	return middleware.Identity{UserID: t.UserID, SessionID: t.SessionID, Access: middleware.UserAccess(t.UserID)}, nil
}

// wsRecheckInterval is how often an open WebSocket re-checks its caller.
var wsRecheckInterval = 30 * time.Second

// wsWatch closes conn once the caller's session or token is gone or perm
// is lost (on res, when given), so revoking a session or an assignment
// also ends open shells and log streams. Stops when the handler returns.
func (h *Handlers) wsWatch(r *http.Request, conn *websocket.Conn, perm rbac.Perm, res *rbac.Resource) {
	t, _ := r.Context().Value(wsTicketKey{}).(*auth.WSTicket)
	if t == nil {
		return
	}
	go func(ctx context.Context) {
		tick := time.NewTicker(wsRecheckInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			id, err := h.wsIdentity(ctx, t)
			if err == nil {
				idCtx := middleware.WithIdentity(ctx, id)
				if (res == nil && middleware.Allowed(idCtx, perm)) || (res != nil && middleware.AllowedOn(idCtx, perm, *res)) {
					continue
				}
			}
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "access revoked"),
				time.Now().Add(time.Second))
			_ = conn.Close()
			return
		}
	}(r.Context())
}

// wsContainerAccess checks perm on the container and returns its resource
// for wsWatch. Writes 403 and returns false when out of scope.
func (h *Handlers) wsContainerAccess(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) (rbac.Resource, bool) {
	scopeReq := h.containerScopeReq(r.Context(), hostID, containerID)
	if !h.allowedOn(r, scopeReq, perm) {
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "container "+containerID)
		return scopeReq, false
	}
	return scopeReq, true
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
	res, allowed := h.wsContainerAccess(w, r, rbac.PermContainersLogs, target.ID(), containerID)
	if !allowed {
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
	h.wsWatch(r, conn, rbac.PermContainersLogs, &res)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Discard incoming messages; a read error means the socket is gone
	// (client left or wsWatch closed it), which also ends the log stream.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	follow := r.URL.Query().Get("follow") != "0"
	rc, err := target.ContainerLogs(ctx, containerID, tail, follow) //nolint:contextcheck // ctx derives from r.Context()
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
			return
		}
	}
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

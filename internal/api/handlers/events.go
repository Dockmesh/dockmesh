package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// WSEvents streams Docker events AND stack filesystem events as JSON messages
// over a WebSocket. Auth via ?ticket= query parameter.
//
// Message shape:
//   Docker:  {"source":"docker", "type":"container", "action":"start", "id":"...", "name":"..."}
//   Stacks:  {"source":"stacks", "type":"modified|created|removed", "name":"...", "file":"compose.yaml"}
func (h *Handlers) WSEvents(w http.ResponseWriter, r *http.Request) {
	r, ok := h.wsAuth(w, r, rbac.PermSystemView)
	if !ok {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws events upgrade failed", "err", err)
		return
	}
	defer conn.Close()
	h.wsWatch(r, conn, rbac.PermSystemView, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	unscoped := h.callerUnscoped(r)

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
					stack:  ev.Actor.Attributes["com.docker.compose.project"],
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
			if !unscoped && !h.inReadScope(r, "local", ev.stack) {
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
			if !unscoped && !h.stackVisible(r, ev.Name) {
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

	stack string
}

// stackVisible reports whether the caller's scope includes the stack on
// its deployment host, or by name alone while it is not deployed.
func (h *Handlers) stackVisible(r *http.Request, name string) bool {
	hostID := ""
	if h.Deployments != nil {
		if d, err := h.Deployments.Get(r.Context(), name); err == nil && d != nil {
			hostID = d.HostID
		}
	}
	if hostID != "" {
		return h.inReadScope(r, hostID, name)
	}
	return h.checkRoleScope(r, rbac.ScopeRequest{StackName: name})
}

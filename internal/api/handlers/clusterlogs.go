package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/swarmread"
)

// maxServiceLogStreams bounds the service log streams open at once across
// all callers; each holds a manager's log stream with every task's logs.
const maxServiceLogStreams = 32

var serviceLogSlots = make(chan struct{}, maxServiceLogStreams)

// maxSince is how far back a relative since may reach.
const maxSince = 90 * 24 * time.Hour

// parseLogsSince turns "" (everything), a duration ("15m", back from now),
// an RFC 3339 time or unix seconds into the unix seconds dockerd takes.
func parseLogsSince(s string, now time.Time) (string, bool) {
	switch {
	case s == "":
		return "", true
	case agents.ValidUnixSeconds(s):
		return s, true
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 || d > maxSince {
			return "", false
		}
		return strconv.FormatInt(now.Add(-d).Unix(), 10), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.Unix() >= 0 {
		return strconv.FormatInt(t.Unix(), 10), true
	}
	return "", false
}

var errLogsTail, errLogsSince = errors.New("bad tail"), errors.New("bad since")

// serviceLogsReqAt reads tail, since and follow of a service log request;
// a relative since counts back from now.
func serviceLogsReqAt(r *http.Request, service string, now time.Time) (agents.SwarmLogsReq, error) {
	q := r.URL.Query()
	req := agents.SwarmLogsReq{Service: service, Tail: 100, Follow: q.Get("follow") != "0" && q.Get("follow") != "false"}
	if t := q.Get("tail"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 || n > agents.MaxSwarmLogTail {
			return req, errLogsTail
		}
		req.Tail = n
	}
	since, ok := parseLogsSince(q.Get("since"), now)
	if !ok {
		return req, errLogsSince
	}
	req.Since = since
	return req, nil
}

// WSClusterServiceLogs streams the logs of every task of a Swarm service,
// read through the cluster's manager, one SwarmLogLine JSON object per
// text message. An error after the upgrade arrives as {"error": "..."}
// before the close. Auth via a containers.logs ticket, checked on the
// cluster's scope.
//
//	GET /api/v1/ws/clusters/{id}/services/{service}/logs
func (h *Handlers) WSClusterServiceLogs(w http.ResponseWriter, r *http.Request) {
	r, ok := h.wsAuth(w, r, rbac.PermContainersLogs)
	if !ok {
		return
	}
	res := rbac.Resource{ClusterID: chi.URLParam(r, "id")}
	if !h.allowedOn(r, res, rbac.PermContainersLogs) {
		h.writeScopeDenied(w, r, rbac.PermContainersLogs, res, "cluster "+res.ClusterID)
		return
	}
	service := chi.URLParam(r, "service")
	if !swarmread.ValidRef(service) {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	req, err := serviceLogsReqAt(r, service, time.Now())
	switch {
	case errors.Is(err, errLogsTail):
		writeError(w, http.StatusBadRequest, "tail must be a number from 0 to 5000")
		return
	case errors.Is(err, errLogsSince):
		writeError(w, http.StatusBadRequest, "since must be a duration like 15m (at most 90 days), an RFC 3339 time or unix seconds")
		return
	}
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	// Resolving the service first answers an unknown one, a worker or an
	// old agent with a status code instead of a failed upgrade.
	d, err := h.Hosts.SwarmService(r.Context(), hostID, service) //nolint:contextcheck // r carries the ticket's identity
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	req.Service = d.ID

	select {
	case serviceLogSlots <- struct{}{}:
	default:
		writeError(w, http.StatusTooManyRequests, "too many service log streams are open; close one and try again")
		return
	}
	defer func() { <-serviceLogSlots }()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()
	h.wsWatch(r, conn, rbac.PermContainersLogs, &res)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	rc, err := h.Hosts.SwarmServiceLogs(ctx, hostID, req) //nolint:contextcheck // ctx derives from r.Context()
	if err != nil {
		writeServiceLogsEnd(conn, err)
		return
	}
	defer rc.Close()
	stop := context.AfterFunc(ctx, func() { _ = rc.Close() }) //nolint:contextcheck // ctx derives from r.Context()
	defer stop()

	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), 8*agents.MaxSwarmLogLine)
	for sc.Scan() {
		var l agents.SwarmLogLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l, ok = agents.SanitizeSwarmLogLine(l); !ok {
			continue
		}
		if err := conn.WriteJSON(l); err != nil {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	writeServiceLogsEnd(conn, sc.Err())
}

// writeServiceLogsEnd closes the socket, after an {"error": …} message
// when the stream did not end cleanly.
func writeServiceLogsEnd(conn *websocket.Conn, err error) {
	if err != nil {
		msg := "the log stream ended: " + err.Error()
		switch {
		case errors.Is(err, agents.ErrNotSwarmManager), errors.Is(err, agents.ErrSwarmServiceNotFound):
			msg = err.Error()
		case errors.Is(err, bufio.ErrTooLong):
			msg = "the log stream sent a line that is too long"
		case errors.Is(err, agents.ErrAgentGone):
			msg = "the manager's agent disconnected"
		default:
			slog.Info("service logs", "err", err)
		}
		_ = conn.WriteJSON(map[string]string{"error": strings.TrimSpace(msg)})
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

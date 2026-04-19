package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/gorilla/websocket"
)

// wsTicket issues a short-lived WS auth ticket via POST /ws/ticket.
// The Dockmesh server uses this pattern because browsers can't attach
// Authorization headers to WebSocket upgrades; dmctl follows the same
// pattern for consistency with the web UI.
func (c *Client) wsTicket() (string, error) {
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := c.request("POST", "/api/v1/ws/ticket", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Ticket, nil
}

// wsURL builds a ws:// or wss:// URL from the server's http(s):// base
// plus the given path + query. Called by log streaming and exec.
func (c *Client) wsURL(path string, q url.Values) string {
	u := c.server + path
	u = strings.Replace(u, "http://", "ws://", 1)
	u = strings.Replace(u, "https://", "wss://", 1)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// dialer matches the http client's TLS posture so dmctl --insecure
// works for self-signed dev servers. Browsers don't need this because
// cookies / CORS already gatekeep; dmctl is strict by default.
func (c *Client) wsDialer() *websocket.Dialer {
	return &websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: c.insecure}, // #nosec G402 — gated by --insecure
	}
}

// streamContainerLogs opens a WebSocket to /ws/logs/{id}?ticket=...
// and copies inbound frames to stdout, line-prefixed when prefix != "".
// Terminates on Ctrl-C, socket close, or an inbound read error.
func streamContainerLogs(c *Client, containerID, tail string, follow bool, prefix string) error {
	ticket, err := c.wsTicket()
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("ticket", ticket)
	if tail != "" {
		q.Set("tail", tail)
	}
	if follow {
		q.Set("follow", "1")
	}
	if flagHost != "" {
		q.Set("host", flagHost)
	}
	u := c.wsURL("/api/v1/ws/logs/"+url.PathEscape(containerID), q)

	conn, resp, err := c.wsDialer().Dial(u, http.Header{})
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("ws dial: %d: %s", resp.StatusCode, trim(string(b), 200))
		}
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Ctrl-C closes the socket cleanly so the server sees EOF.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		case <-done:
		}
	}()
	defer close(done)
	defer signal.Stop(sigCh)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			// Normal end-of-stream when container exits: treat as nil.
			if strings.Contains(err.Error(), "use of closed") {
				return nil
			}
			return err
		}
		writePrefixed(os.Stdout, prefix, msg)
	}
}

// writePrefixed prepends prefix to every line in msg before writing to w.
// Keeps partial-line chunks intact — the server typically sends whole
// lines so this is mostly cosmetic.
func writePrefixed(w io.Writer, prefix string, msg []byte) {
	if prefix == "" {
		_, _ = w.Write(msg)
		if len(msg) > 0 && msg[len(msg)-1] != '\n' {
			_, _ = w.Write([]byte("\n"))
		}
		return
	}
	for _, line := range strings.Split(strings.TrimRight(string(msg), "\n"), "\n") {
		_, _ = fmt.Fprintln(w, prefix+line)
	}
}

// resizeMsg is the JSON control frame sent over exec WS when the
// caller's terminal changes size. Matches the server-side parser in
// internal/api/handlers/exec.go.
type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// marshalResize is extracted so the exec command can send resize
// frames without duplicating the JSON shape.
func marshalResize(cols, rows uint16) ([]byte, error) {
	return json.Marshal(resizeMsg{Type: "resize", Cols: cols, Rows: rows})
}

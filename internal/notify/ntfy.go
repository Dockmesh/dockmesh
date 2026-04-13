package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type ntfyCfg struct {
	URL      string `json:"url"`            // e.g. https://ntfy.sh/dockmesh-alerts
	Token    string `json:"token,omitempty"` // optional auth for self-hosted ntfy
	Priority int    `json:"priority,omitempty"` // 1-5, defaults to 3 (default)
}

type ntfyChannel struct {
	cfg  ntfyCfg
	http *http.Client
}

func parseNtfy(raw json.RawMessage, hc *http.Client) (*ntfyChannel, error) {
	var cfg ntfyCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("ntfy url required (e.g. https://ntfy.sh/your-topic)")
	}
	return &ntfyChannel{cfg: cfg, http: hc}, nil
}

// Send pushes the notification to the configured topic URL. ntfy uses
// header-based metadata: X-Title, X-Tags, X-Priority. Body is plain text.
func (c *ntfyChannel) Send(ctx context.Context, n Notification) error {
	body := n.Body
	if body == "" {
		body = n.Title
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if n.Title != "" {
		req.Header.Set("Title", n.Title)
	}
	prio := c.cfg.Priority
	if prio == 0 {
		prio = priorityFromLevel(n.Level)
	}
	req.Header.Set("Priority", strconv.Itoa(prio))
	req.Header.Set("Tags", strings.Join(tagsFromLevel(n.Level), ","))
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%d %s: %s", resp.StatusCode, resp.Status, string(b))
	}
	return nil
}

func priorityFromLevel(l Level) int {
	switch l {
	case LevelCritical:
		return 5
	case LevelWarning:
		return 4
	default:
		return 3
	}
}

func tagsFromLevel(l Level) []string {
	switch l {
	case LevelCritical:
		return []string{"rotating_light", "dockmesh"}
	case LevelWarning:
		return []string{"warning", "dockmesh"}
	default:
		return []string{"information_source", "dockmesh"}
	}
}

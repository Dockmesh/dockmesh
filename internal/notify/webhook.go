package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type webhookCfg struct {
	URL string `json:"url"`
}

type webhookChannel struct {
	cfg  webhookCfg
	http *http.Client
}

func parseWebhook(raw json.RawMessage, hc *http.Client) (*webhookChannel, error) {
	var cfg webhookCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("webhook url required")
	}
	return &webhookChannel{cfg: cfg, http: hc}, nil
}

// Send POSTs the raw Notification as JSON. This is the lowest-friction
// integration: any HTTP listener that can parse JSON works, and it lets
// users hand-roll templates for Telegram, n8n, Zapier, Home Assistant…
func (c *webhookChannel) Send(ctx context.Context, n Notification) error {
	body, err := json.Marshal(map[string]any{
		"title":     n.Title,
		"body":      n.Body,
		"level":     n.Level,
		"container": n.Container,
		"metric":    n.Metric,
		"value":     n.Value,
		"threshold": n.Threshold,
		"timestamp": n.Time.Unix(),
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, c.http, c.cfg.URL, body)
}

// postJSON is a small helper used by every HTTP-based channel.
func postJSON(ctx context.Context, hc *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
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

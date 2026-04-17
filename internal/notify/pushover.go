package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Pushover — https://pushover.net/api
//
// Requires two tokens: the "app token" (per-application, created at
// pushover.net) and the "user key" (per-recipient). We post form-
// encoded, same shape the official CLIs use.
const pushoverAPIURL = "https://api.pushover.net/1/messages.json"

type pushoverCfg struct {
	AppToken string `json:"app_token"`
	UserKey  string `json:"user_key"`
	Device   string `json:"device,omitempty"` // optional — target a single device
	Sound    string `json:"sound,omitempty"`  // optional — pushover sound name
}

type pushoverChannel struct {
	cfg  pushoverCfg
	http *http.Client
}

func parsePushover(raw json.RawMessage, hc *http.Client) (*pushoverChannel, error) {
	var cfg pushoverCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.AppToken) == "" {
		return nil, errors.New("pushover app_token required")
	}
	if strings.TrimSpace(cfg.UserKey) == "" {
		return nil, errors.New("pushover user_key required")
	}
	return &pushoverChannel{cfg: cfg, http: hc}, nil
}

func (c *pushoverChannel) Send(ctx context.Context, n Notification) error {
	body := n.Body
	if n.Container != "" {
		body += fmt.Sprintf("\nContainer: %s", n.Container)
	}
	if n.Metric != "" {
		body += fmt.Sprintf("\n%s: %s (threshold %s)", n.Metric, formatValue(n.Value), formatValue(n.Threshold))
	}

	form := url.Values{}
	form.Set("token", c.cfg.AppToken)
	form.Set("user", c.cfg.UserKey)
	form.Set("title", n.Title)
	form.Set("message", body)
	// Pushover priority: -2 lowest, 0 default, 1 high (visual alert on device),
	// 2 emergency (requires retry/expire). Critical → 1 is as far as we
	// push by default; emergency 2 would need extra UI + acknowledgement
	// handling, better left to the operator to enable manually via a
	// dedicated channel if they need it.
	form.Set("priority", pushoverPriority(n.Level))
	if c.cfg.Device != "" {
		form.Set("device", c.cfg.Device)
	}
	if c.cfg.Sound != "" {
		form.Set("sound", c.cfg.Sound)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverAPIURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pushover %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func pushoverPriority(l Level) string {
	switch l {
	case LevelCritical:
		return "1"
	case LevelWarning:
		return "0"
	default:
		return "-1"
	}
}

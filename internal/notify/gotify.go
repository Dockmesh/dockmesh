package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type gotifyCfg struct {
	URL   string `json:"url"`   // e.g. https://gotify.example.com
	Token string `json:"token"` // application token
}

type gotifyChannel struct {
	cfg  gotifyCfg
	http *http.Client
}

func parseGotify(raw json.RawMessage, hc *http.Client) (*gotifyChannel, error) {
	var cfg gotifyCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" || cfg.Token == "" {
		return nil, errors.New("gotify url and token required")
	}
	return &gotifyChannel{cfg: cfg, http: hc}, nil
}

func (c *gotifyChannel) Send(ctx context.Context, n Notification) error {
	body, err := json.Marshal(map[string]any{
		"title":    n.Title,
		"message":  n.Body,
		"priority": gotifyPriority(n.Level),
	})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.cfg.URL, "/") + "/message?token=" + c.cfg.Token
	return postJSON(ctx, c.http, endpoint, body)
}

func gotifyPriority(l Level) int {
	switch l {
	case LevelCritical:
		return 8
	case LevelWarning:
		return 5
	default:
		return 3
	}
}

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type discordCfg struct {
	URL string `json:"url"`
}

type discordChannel struct {
	cfg  discordCfg
	http *http.Client
}

func parseDiscord(raw json.RawMessage, hc *http.Client) (*discordChannel, error) {
	var cfg discordCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("discord webhook url required")
	}
	return &discordChannel{cfg: cfg, http: hc}, nil
}

func (c *discordChannel) Send(ctx context.Context, n Notification) error {
	color := discordColor(n.Level)
	embed := map[string]any{
		"title":       n.Title,
		"description": n.Body,
		"color":       color,
		"timestamp":   n.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
	fields := []map[string]any{}
	if n.Container != "" {
		fields = append(fields, map[string]any{"name": "Container", "value": n.Container, "inline": true})
	}
	if n.Metric != "" {
		fields = append(fields, map[string]any{"name": "Metric", "value": n.Metric, "inline": true})
	}
	if n.Threshold != 0 {
		fields = append(fields, map[string]any{
			"name":   "Value / Threshold",
			"value":  formatValue(n.Value) + " / " + formatValue(n.Threshold),
			"inline": true,
		})
	}
	if len(fields) > 0 {
		embed["fields"] = fields
	}
	body, err := json.Marshal(map[string]any{
		"username": "Dockmesh",
		"embeds":   []any{embed},
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, c.http, c.cfg.URL, body)
}

func discordColor(l Level) int {
	switch l {
	case LevelCritical:
		return 0xEF4444 // red-500
	case LevelWarning:
		return 0xEAB308 // yellow-500
	default:
		return 0x06B6D4 // brand cyan
	}
}

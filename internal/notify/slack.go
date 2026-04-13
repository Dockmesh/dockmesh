package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type slackCfg struct {
	URL string `json:"url"`
}

type slackChannel struct {
	cfg  slackCfg
	http *http.Client
}

func parseSlack(raw json.RawMessage, hc *http.Client) (*slackChannel, error) {
	var cfg slackCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("slack webhook url required")
	}
	return &slackChannel{cfg: cfg, http: hc}, nil
}

func (c *slackChannel) Send(ctx context.Context, n Notification) error {
	emoji := slackEmoji(n.Level)
	header := fmt.Sprintf("%s %s", emoji, n.Title)
	mrkdwn := n.Body
	if n.Container != "" {
		mrkdwn += fmt.Sprintf("\n*Container:* `%s`", n.Container)
	}
	if n.Metric != "" {
		mrkdwn += fmt.Sprintf("\n*%s:* `%s` (threshold `%s`)",
			n.Metric, formatValue(n.Value), formatValue(n.Threshold))
	}
	body, err := json.Marshal(map[string]any{
		"text": header,
		"blocks": []any{
			map[string]any{
				"type": "header",
				"text": map[string]any{"type": "plain_text", "text": header},
			},
			map[string]any{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": mrkdwn},
			},
		},
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, c.http, c.cfg.URL, body)
}

func slackEmoji(l Level) string {
	switch l {
	case LevelCritical:
		return ":rotating_light:"
	case LevelWarning:
		return ":warning:"
	default:
		return ":information_source:"
	}
}

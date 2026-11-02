package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/i18n"
)

type teamsCfg struct {
	URL string `json:"url"`
}

type teamsChannel struct {
	cfg  teamsCfg
	http *http.Client
}

func parseTeams(raw json.RawMessage, hc *http.Client) (*teamsChannel, error) {
	var cfg teamsCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("teams webhook url required")
	}
	return &teamsChannel{cfg: cfg, http: hc}, nil
}

// Send posts a MessageCard payload. Teams' incoming-webhook connector
// supports both MessageCard (legacy, simpler) and Adaptive Cards. We use
// MessageCard because every existing Teams webhook URL accepts it.
func (c *teamsChannel) Send(ctx context.Context, n Notification) error {
	facts := []map[string]string{}
	if n.Container != "" {
		facts = append(facts, map[string]string{"name": i18n.T(n.Lang, "notify.container"), "value": n.Container})
	}
	if n.Metric != "" {
		facts = append(facts, map[string]string{
			"name":  n.Metric,
			"value": fmt.Sprintf("%s (%s %s)", formatValue(n.Value), i18n.T(n.Lang, "notify.threshold"), formatValue(n.Threshold)),
		})
	}
	body, err := json.Marshal(map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"themeColor": teamsColor(n.Level),
		"summary":    n.Title,
		"sections": []any{
			map[string]any{
				"activityTitle":    n.Title,
				"activitySubtitle": n.Time.UTC().Format("2006-01-02 15:04:05 UTC"),
				"text":             n.Body,
				"facts":            facts,
			},
		},
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, c.http, c.cfg.URL, body)
}

func teamsColor(l Level) string {
	switch l {
	case LevelCritical:
		return "EF4444"
	case LevelWarning:
		return "EAB308"
	default:
		return "06B6D4"
	}
}

package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// PagerDuty Events API v2. One integration key (= "routing key") is
// tied to one service in PD; all Dockmesh alerts to this channel will
// appear against that service. The dedup_key we send lets PD
// auto-resolve alerts when we send a `resolve` event for the same key
// — so one container flapping doesn't page somebody every 30 seconds.
//
// Docs: https://developer.pagerduty.com/docs/events-api-v2/overview
const pagerDutyEventsURL = "https://events.pagerduty.com/v2/enqueue"

type pagerDutyCfg struct {
	IntegrationKey string `json:"integration_key"`
	// Optional: override the client name shown in PD ("Dockmesh" by default).
	Client    string `json:"client,omitempty"`
	ClientURL string `json:"client_url,omitempty"`
}

type pagerDutyChannel struct {
	cfg  pagerDutyCfg
	http *http.Client
}

func parsePagerDuty(raw json.RawMessage, hc *http.Client) (*pagerDutyChannel, error) {
	var cfg pagerDutyCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.IntegrationKey) == "" {
		return nil, errors.New("pagerduty integration_key required")
	}
	return &pagerDutyChannel{cfg: cfg, http: hc}, nil
}

func (c *pagerDutyChannel) Send(ctx context.Context, n Notification) error {
	// PagerDuty wants a short stable dedup key so repeated fires of
	// the same rule against the same container fold into one alert.
	// We hash title+container so the key stays under PD's 255-char
	// cap even for long rule/container names.
	sum := sha256.Sum256([]byte(n.Title + "|" + n.Container))
	dedup := "dockmesh-" + hex.EncodeToString(sum[:])[:16]

	client := c.cfg.Client
	if client == "" {
		client = "Dockmesh"
	}

	summary := n.Title
	if n.Container != "" {
		summary = fmt.Sprintf("%s — %s", n.Title, n.Container)
	}
	custom := map[string]any{}
	if n.Metric != "" {
		custom["metric"] = n.Metric
		custom["value"] = formatValue(n.Value)
		custom["threshold"] = formatValue(n.Threshold)
	}
	if n.Body != "" {
		custom["body"] = n.Body
	}

	payload := map[string]any{
		"routing_key":  c.cfg.IntegrationKey,
		"event_action": "trigger",
		"dedup_key":    dedup,
		"client":       client,
	}
	if c.cfg.ClientURL != "" {
		payload["client_url"] = c.cfg.ClientURL
	}
	payload["payload"] = map[string]any{
		"summary":        summary,
		"source":         stringOr(n.Container, "dockmesh"),
		"severity":       pagerDutySeverity(n.Level),
		"component":      n.Metric,
		"timestamp":      n.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
		"custom_details": custom,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return postJSON(ctx, c.http, pagerDutyEventsURL, body)
}

// pagerDutySeverity maps Dockmesh's 3-level scheme to PD's 4 values.
// Dockmesh has no "error" level — warning covers that territory.
func pagerDutySeverity(l Level) string {
	switch l {
	case LevelCritical:
		return "critical"
	case LevelWarning:
		return "warning"
	default:
		return "info"
	}
}

func stringOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

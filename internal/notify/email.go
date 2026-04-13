package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/smtp"
	"strconv"
	"strings"
)

type emailCfg struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

type emailChannel struct {
	cfg emailCfg
}

func parseEmail(raw json.RawMessage) (*emailChannel, error) {
	var cfg emailCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Host == "" || cfg.From == "" || len(cfg.To) == 0 {
		return nil, errors.New("smtp host, from and to required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &emailChannel{cfg: cfg}, nil
}

func (c *emailChannel) Send(ctx context.Context, n Notification) error {
	addr := c.cfg.Host + ":" + strconv.Itoa(c.cfg.Port)
	subject := n.Title
	if subject == "" {
		subject = "Dockmesh alert"
	}
	body := n.Body
	if n.Container != "" {
		body += fmt.Sprintf("\n\nContainer: %s", n.Container)
	}
	if n.Metric != "" {
		body += fmt.Sprintf("\n%s: %s (threshold %s)", n.Metric, formatValue(n.Value), formatValue(n.Threshold))
	}
	body += fmt.Sprintf("\n\nTime: %s", n.Time.UTC().Format("2006-01-02 15:04:05 UTC"))

	msg := strings.Builder{}
	msg.WriteString("From: " + c.cfg.From + "\r\n")
	msg.WriteString("To: " + strings.Join(c.cfg.To, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(body)

	var auth smtp.Auth
	if c.cfg.Username != "" {
		auth = smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	}
	return smtp.SendMail(addr, auth, c.cfg.From, c.cfg.To, []byte(msg.String()))
}

// formatValue is shared by every channel that surfaces a metric value.
func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"sort"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/i18n"
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
	cfg    emailCfg
	lang   string
	langOf func(ctx context.Context, addr string) (string, error)
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

// Send mails each recipient in their own language: the language of the
// Dockmesh user with that address, else the channel's.
func (c *emailChannel) Send(ctx context.Context, n Notification) error {
	addr := c.cfg.Host + ":" + strconv.Itoa(c.cfg.Port)
	var auth smtp.Auth
	if c.cfg.Username != "" {
		auth = smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	}
	groups := c.recipientsByLanguage(ctx)
	langs := make([]string, 0, len(groups))
	for l := range groups {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		to := groups[l]
		if err := smtp.SendMail(addr, auth, c.cfg.From, to, c.message(n.in(l), to)); err != nil {
			return err
		}
	}
	return nil
}

func (c *emailChannel) recipientsByLanguage(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	for _, a := range c.cfg.To {
		lang := c.lang
		if c.langOf != nil {
			if l, err := c.langOf(ctx, a); err != nil {
				slog.Debug("notify: recipient language", "err", err)
			} else if l != "" {
				lang = l
			}
		}
		lang = i18n.Normalize(lang)
		out[lang] = append(out[lang], a)
	}
	return out
}

func (c *emailChannel) message(n Notification, recipients []string) []byte {
	lang := n.Lang
	subject := n.Title
	if subject == "" {
		subject = i18n.T(lang, "notify.default_subject")
	}
	body := n.Body
	if n.Container != "" {
		body += fmt.Sprintf("\n\n%s: %s", i18n.T(lang, "notify.container"), n.Container)
	}
	if n.Metric != "" {
		body += fmt.Sprintf("\n%s: %s (%s %s)", n.Metric, formatValue(n.Value), i18n.T(lang, "notify.threshold"), formatValue(n.Threshold))
	}
	body += fmt.Sprintf("\n\n%s: %s", i18n.T(lang, "notify.time"), n.Time.UTC().Format("2006-01-02 15:04:05 UTC"))

	to := make([]string, len(recipients))
	for i, a := range recipients {
		to[i] = headerValue(a)
	}
	msg := strings.Builder{}
	msg.WriteString("From: " + headerValue(c.cfg.From) + "\r\n")
	msg.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	msg.WriteString("Subject: " + headerValue(subject) + "\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(body)
	return []byte(msg.String())
}

// headerValue flattens v to one line. Rule names and alert titles are set
// by users who must not be able to add headers or end the header block.
func headerValue(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// formatValue is shared by every channel that surfaces a metric value.
func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

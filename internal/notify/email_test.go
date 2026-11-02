package notify

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEmailMessageHeaderInjection(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"crlf bcc", "x\r\nBcc: attacker@example.com"},
		{"lf only", "x\nBcc: attacker@example.com"},
		{"blank line then body", "x\r\n\r\nphishing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &emailChannel{cfg: emailCfg{From: "a@example.com\r\nX-Evil: 1", To: []string{"b@example.com\nBcc: c@example.com"}}}
			msg := string(c.message(Notification{Title: tt.title, Body: "body", Time: time.Unix(0, 0)}, c.cfg.To))
			head, _, ok := strings.Cut(msg, "\r\n\r\n")
			if !ok {
				t.Fatal("no header/body separator")
			}
			for _, line := range strings.Split(head, "\r\n") {
				switch {
				case strings.HasPrefix(line, "From: "), strings.HasPrefix(line, "To: "),
					strings.HasPrefix(line, "Subject: "), strings.HasPrefix(line, "Content-Type: "):
				default:
					t.Errorf("injected header line %q", line)
				}
			}
			if strings.Contains(head, "\n\n") || strings.Contains(strings.ReplaceAll(head, "\r\n", ""), "\n") {
				t.Errorf("stray newline in headers: %q", head)
			}
		})
	}
}

func TestEmailRecipientLanguages(t *testing.T) {
	users := map[string]string{"anna@example.com": "de", "carl@example.com": "fr"}
	lookup := func(_ context.Context, addr string) (string, error) {
		if addr == "dora@example.com" {
			return "de", errors.New("db closed")
		}
		return users[addr], nil
	}
	cases := []struct {
		name    string
		channel string
		want    map[string][]string
	}{
		{"channel english", "", map[string][]string{
			"de": {"anna@example.com"},
			"en": {"ben@example.com", "carl@example.com", "dora@example.com"},
		}},
		{"channel german", "de", map[string][]string{
			"de": {"anna@example.com", "ben@example.com", "dora@example.com"},
			"en": {"carl@example.com"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &emailChannel{
				cfg:    emailCfg{To: []string{"anna@example.com", "ben@example.com", "carl@example.com", "dora@example.com"}},
				lang:   tc.channel,
				langOf: lookup,
			}
			got := c.recipientsByLanguage(context.Background())
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmailMessageGerman(t *testing.T) {
	c := &emailChannel{cfg: emailCfg{From: "a@example.com"}}
	n := Notification{
		Title: "CPU", Body: "high", Container: "web", Metric: "cpu_percent", Value: 91, Threshold: 80,
		Time:     time.Unix(0, 0),
		Localize: func(lang string) (string, string) { return "CPU " + lang, "hoch" },
	}
	msg := string(c.message(n.in("de"), []string{"b@example.com"}))
	for _, want := range []string{"Subject: CPU de", "hoch", "Container: web", "(Schwellwert 80.0)", "Zeit: 1970-01-01"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

package notify

import (
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
			msg := string(c.message(Notification{Title: tt.title, Body: "body", Time: time.Unix(0, 0)}))
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

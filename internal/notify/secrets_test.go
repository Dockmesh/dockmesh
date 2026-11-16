package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Every field of a channel config struct is either public or a credential
// on purpose: a new field must be added to one of the two lists.
func TestConfigFieldsClassified(t *testing.T) {
	tests := []struct {
		typ     string
		cfg     any
		secrets []string
	}{
		{"webhook", webhookCfg{}, []string{"url"}},
		{"discord", discordCfg{}, []string{"url"}},
		{"slack", slackCfg{}, []string{"url"}},
		{"teams", teamsCfg{}, []string{"url"}},
		{"ntfy", ntfyCfg{}, []string{"url", "token"}},
		{"gotify", gotifyCfg{}, []string{"url", "token"}},
		{"email", emailCfg{}, []string{"password"}},
		{"pagerduty", pagerDutyCfg{}, []string{"integration_key"}},
		{"pushover", pushoverCfg{}, []string{"app_token", "user_key"}},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if !validType(tt.typ) {
				t.Fatalf("%s is not a channel type", tt.typ)
			}
			isSecret := map[string]bool{}
			for _, s := range tt.secrets {
				isSecret[s] = true
			}
			rt := reflect.TypeOf(tt.cfg)
			for i := range rt.NumField() {
				name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
				if got := PublicConfigField(tt.typ, name); got == isSecret[name] {
					t.Errorf("field %q: public = %v, want %v", name, got, !isSecret[name])
				}
			}
			if !PublicConfigField(tt.typ, "language") {
				t.Error("language must be public")
			}
			for _, k := range []string{"secret", "headers", "url_set"} {
				if PublicConfigField(tt.typ, k) {
					t.Errorf("unknown field %q must not be public", k)
				}
			}
		})
	}
}

func TestErrorTextMasksSecrets(t *testing.T) {
	c := &Channel{Type: "gotify", Config: json.RawMessage(
		`{"url":"https://push.example.com/base","token":"tok-123456","language":"de","n":5,"x":"ab"}`)}
	secrets := secretValues(c)
	tests := []struct {
		name     string
		err      error
		withBody bool
		want     string
	}{
		{"plain", errors.New("bad token tok-123456"), false, "bad token [redacted]"},
		{"echoed path", &statusError{Status: "404 Not Found", Body: "Cannot POST /base/message"}, true, "404 Not Found: Cannot POST [redacted]/message"},
		{"status only", &statusError{Status: "404 Not Found", Body: "tok-123456"}, false, "404 Not Found"},
		{"public value kept", errors.New("language de"), false, "language de"},
		{"short value kept", errors.New("ab"), false, "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorText(tt.err, tt.withBody, secrets); got != tt.want {
				t.Errorf("errorText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSendToMasksEchoedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Cannot POST "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()

	s := newNotifyService(t)
	id := addWebhook(t, s, srv.URL+"/hooks/secret-token")
	err := s.SendTo(context.Background(), id, Notification{Title: "t"})
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("SendTo error = %v, want the status without the token", err)
	}
}

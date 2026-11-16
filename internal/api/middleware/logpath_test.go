package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/api/v1/invite/abc123", "/api/v1/invite/[redacted]"},
		{"/api/v1/invite/abc123/accept", "/api/v1/invite/[redacted]/accept"},
		{"/invite/abc123", "/invite/[redacted]"},
		{"/api/v1/invite/abc123/other/x", "/api/v1/invite/[redacted]/[redacted]/[redacted]"},
		{"/api/v1/invite//abc123", "/api/v1/invite//[redacted]"},
		{"//invite/abc123/", "//invite/[redacted]/"},
		{"/API/V1/Invite/abc123", "/API/V1/Invite/[redacted]"},
		{"/api/v1/invite/", "/api/v1/invite/"},
		{"/api/v1/invite", "/api/v1/invite"},
		{"/api/v1/invites", "/api/v1/invites"},
		{"/api/v1/invites/7", "/api/v1/invites/7"},
		{"/api/v1/containers/abc", "/api/v1/containers/abc"},
		{"/", "/"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := RedactPath(tt.in); got != tt.want {
				t.Errorf("RedactPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoggingRedactsInviteToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := Logging(http.NotFoundHandler())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/invite/tok-secret/accept/x", nil))
	if out := buf.String(); strings.Contains(out, "tok-secret") || !strings.Contains(out, "/api/v1/invite/[redacted]/accept") {
		t.Fatalf("log line = %q", out)
	}
}

func TestHidePathSecrets(t *testing.T) {
	var traced, served string
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traced = r.URL.Path
			next.ServeHTTP(w, r)
		})
	}
	h := HidePathSecrets(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = r.URL.Path
	}), wrap)
	for _, p := range []string{"/invite/tok-secret", "/api/v1/containers"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
		if served != p {
			t.Errorf("handler saw %q, want %q", served, p)
		}
		if traced != RedactPath(p) {
			t.Errorf("tracer saw %q, want %q", traced, RedactPath(p))
		}
	}
}

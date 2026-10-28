package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
)

func newNotifyService(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	s := NewService(database)
	s.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return s
}

func addWebhook(t *testing.T, s *Service, url string) int64 {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"url": url})
	c, err := s.Create(context.Background(), ChannelInput{Type: "webhook", Name: "hook", Config: cfg, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestSendWithRetry(t *testing.T) {
	tests := []struct {
		name      string
		failFirst int32
		wantCalls int32
		wantErr   bool
	}{
		{"first attempt succeeds", 0, 1, false},
		{"recovers on third attempt", 2, 3, false},
		{"gives up after three attempts", 10, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) <= tt.failFirst {
					http.Error(w, "boom", http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			s := newNotifyService(t)
			id := addWebhook(t, s, srv.URL)
			err := s.sendWithRetry(context.Background(), id, Notification{Title: "t"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", got, tt.wantCalls)
			}

			if err := s.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			c := s.byID(id)
			if tt.wantErr {
				if c.LastErrorAt == nil || !strings.Contains(c.LastError, "502") {
					t.Fatalf("last error not persisted: %+v", c)
				}
			} else if c.LastSentAt == nil || c.LastErrorAt != nil {
				t.Fatalf("success not persisted: %+v", c)
			}
		})
	}
}

func TestRecordedErrorOmitsURL(t *testing.T) {
	s := newNotifyService(t)
	id := addWebhook(t, s, "http://127.0.0.1:1/hooks/secret-token")
	if err := s.sendWithRetry(context.Background(), id, Notification{Title: "t"}); err == nil {
		t.Fatal("expected connection error")
	}
	c := s.byID(id)
	if c.LastError == "" || strings.Contains(c.LastError, "secret-token") {
		t.Fatalf("last_error = %q, want non-empty without the URL", c.LastError)
	}
}

func TestSendWithRetrySkipsDisabled(t *testing.T) {
	s := newNotifyService(t)
	cfg, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:1/"})
	c, err := s.Create(context.Background(), ChannelInput{Type: "webhook", Name: "off", Config: cfg, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sendWithRetry(context.Background(), c.ID, Notification{}); err == nil {
		t.Fatal("expected disabled error")
	}
	if got := s.byID(c.ID); got.LastErrorAt != nil {
		t.Fatalf("disabled channel must not record a delivery error: %+v", got)
	}
}

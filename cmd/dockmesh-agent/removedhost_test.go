package main

import (
	"bytes"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDialErrorRecognisesDeletedHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		body    string
		removed bool
	}{
		{"deleted host", http.StatusUnauthorized, "agent not registered", true},
		{"other 401", http.StatusUnauthorized, "client certificate required", false},
		{"revoked", http.StatusForbidden, "agent revoked", false},
		{"lookup failure", http.StatusServiceUnavailable, "try again later", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tc.body, tc.code)
			}))
			defer srv.Close()
			_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err == nil {
				t.Fatal("handshake succeeded")
			}
			got := dialError(resp, err)
			if errors.Is(got, errHostRemoved) != tc.removed {
				t.Fatalf("dialError = %v, want removed=%v", got, tc.removed)
			}
		})
	}
	if err := dialError(nil, errors.New("connection refused")); errors.Is(err, errHostRemoved) {
		t.Fatal("a refused connection counted as a deleted host")
	}
}

func TestReconnectsAfterDeletedHost(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test jitter

	var rc reconnects
	for i := 0; i < 5; i++ {
		if d := rc.next(errHostRemoved, time.Millisecond, rng); d != removedRetry {
			t.Fatalf("attempt %d: delay %s, want %s", i, d, removedRetry)
		}
	}
	if n := strings.Count(logs.String(), "This host was removed from Dockmesh. Run: sudo dockmesh-agent uninstall"); n != 1 {
		t.Fatalf("logged the removal %d times, want once:\n%s", n, logs.String())
	}
	if removedRetry != time.Hour {
		t.Fatalf("removedRetry = %s, want 1h outside lab builds", removedRetry)
	}

	// The host is back (re-enrolled): normal backoff, and a later removal
	// is logged again.
	if d := rc.next(errors.New("dial: EOF"), time.Millisecond, rng); d != 0 {
		t.Fatalf("first retry after the host came back waited %s", d)
	}
	rc.next(errHostRemoved, time.Millisecond, rng)
	if n := strings.Count(logs.String(), "This host was removed"); n != 2 {
		t.Fatalf("a second removal logged %d lines in total, want 2", n)
	}
}

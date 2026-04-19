package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestClientRequest_Success exercises the happy path: bearer auth,
// JSON body round-trip, 2xx → decoded out.
func TestClientRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t-xyz" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/things" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("x") != "1" {
			http.Error(w, "no query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "n": 3})
	}))
	defer srv.Close()

	c := &Client{server: srv.URL, token: "t-xyz", http: srv.Client()}
	var out struct {
		OK bool `json:"ok"`
		N  int  `json:"n"`
	}
	if err := c.request("GET", "/api/v1/things", url.Values{"x": {"1"}}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.N != 3 {
		t.Fatalf("got %+v", out)
	}
}

// TestClientRequest_ErrorEnvelope verifies the error surfaced to the
// user prefers the server's structured `error` field over raw body.
func TestClientRequest_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "cycle detected: a -> b -> a"})
	}))
	defer srv.Close()

	c := &Client{server: srv.URL, token: "t", http: srv.Client()}
	err := c.request("POST", "/api/v1/dependencies", nil, map[string]any{}, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if got := err.Error(); got == "" || !contains(got, "cycle detected") {
		t.Fatalf("error missing detail: %s", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

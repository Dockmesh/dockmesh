package notify

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckChannel(t *testing.T) {
	tests := []struct {
		name, typ, config string
		ok                bool
	}{
		{"webhook", "webhook", `{"url":"https://hooks.example.org/x"}`, true},
		{"unknown type", "carrier-pigeon", `{}`, false},
		{"foreign field", "webhook", `{"url":"https://h.example.org","host":"x"}`, false},
		{"missing field", "email", `{"host":"smtp.example.org"}`, false},
		{"email", "email", `{"host":"smtp.example.org","from":"dm@example.org","to":["ops@example.org"]}`, true},
		{"not an object", "webhook", `[1]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckChannel(tt.typ, json.RawMessage(tt.config)); (err == nil) != tt.ok {
				t.Errorf("CheckChannel = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestSendTestMasksSecrets(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if strings.Contains(r.URL.Path, "fail") {
			http.Error(w, "bad token at "+r.URL.Path, http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	n := Notification{Title: "Test", Body: "hello", Level: LevelInfo, Time: time.Now()}
	if err := SendTest(context.Background(), srv.Client(), nil, "webhook", json.RawMessage(`{"url":"`+srv.URL+`/ok"}`), n); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("body %q", got)
	}
	err := SendTest(context.Background(), srv.Client(), nil, "webhook", json.RawMessage(`{"url":"`+srv.URL+`/fail-s3cr3tpath"}`), n)
	if err == nil || strings.Contains(err.Error(), "s3cr3tpath") {
		t.Errorf("error %v", err)
	}
}

func TestSendTestEmailEndsAtDeadline(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// Accepts and never greets, like a tarpit.
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	cfg := json.RawMessage(`{"host":"127.0.0.1","port":` + port + `,"from":"a@example.org","to":["b@example.org"]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = SendTest(ctx, nil, (&net.Dialer{}).DialContext, "email", cfg, Notification{Title: "t", Time: time.Now()})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("SendTest = %v after %v", err, time.Since(start))
	}
}

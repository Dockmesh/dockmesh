package targets

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestIdleConnKeepsProgressingTransfersAlive(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }()

	c := &idleConn{Conn: a, idle: 150 * time.Millisecond}
	// Total time exceeds the idle limit, but no single gap does.
	for i := 0; i < 6; i++ {
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(60 * time.Millisecond)
	}
}

func TestIdleConnFailsOnStall(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	c := &idleConn{Conn: a, idle: 50 * time.Millisecond}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on a silent peer did not time out")
	}
}

func TestWebDAVUploadRoundTrip(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			files[r.URL.Path] = body
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case "GET":
			mu.Lock()
			body, ok := files[r.URL.Path]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	wd, err := NewWebDAV(map[string]any{"url": srv.URL, "username": "u", "password": "p", "path": "/backups"})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("dockmesh"), 1<<20)
	w, err := wd.Open(context.Background(), "a/b.tar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := wd.Read(context.Background(), "a/b.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read back %d bytes, want %d identical", len(got), len(payload))
	}
}

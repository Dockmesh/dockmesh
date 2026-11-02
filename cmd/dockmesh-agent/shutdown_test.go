package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCloseOnDoneUnblocksRead(t *testing.T) {
	closeCode := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, _, err = c.ReadMessage()
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			closeCode <- ce.Code
		} else {
			closeCode <- -1
		}
	}))
	t.Cleanup(srv.Close)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	stop := closeOnDone(ctx, conn)
	defer stop()

	readDone := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage()
		readDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("read returned without an error after shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked read did not return after the context was cancelled")
	}
	select {
	case code := <-closeCode:
		if code != websocket.CloseGoingAway {
			t.Errorf("server saw close code %d, want %d (going away)", code, websocket.CloseGoingAway)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the connection close")
	}
}

func TestCloseOnDoneStopKeepsConnection(t *testing.T) {
	conn, frames := wsPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := closeOnDone(ctx, conn)
	if !stop() {
		t.Fatal("stop reported the watch had already fired")
	}
	cancel()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"agent.pong"}`)); err != nil {
		t.Fatalf("connection closed although the watch was stopped: %v", err)
	}
	recvFrame(t, frames)
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitBackupRun(t *testing.T) {
	tests := []struct {
		name       string
		running    int32 // polls answered with status running
		failFirst  int32 // polls answered with 503 before that
		wantStatus string
		wantErr    bool
	}{
		{name: "finished at once", wantStatus: "success"},
		{name: "running then success", running: 3, wantStatus: "success"},
		{name: "survives a restart", failFirst: 2, running: 1, wantStatus: "success"},
		{name: "server gone", failFirst: 100, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var polls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/backups/runs/7" {
					http.NotFound(w, r)
					return
				}
				n := polls.Add(1)
				if n <= tt.failFirst {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				status := "success"
				if n <= tt.failFirst+tt.running {
					status = "running"
				}
				_ = json.NewEncoder(w).Encode(backupRun{ID: 7, Status: status, SizeBytes: 42})
			}))
			defer srv.Close()

			c := &Client{server: srv.URL, token: "t", http: srv.Client()}
			run, err := waitBackupRun(context.Background(), c, 7, time.Millisecond)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got run %+v", run)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != tt.wantStatus || run.SizeBytes != 42 {
				t.Errorf("run = %+v, want status %s", run, tt.wantStatus)
			}
			if want := tt.failFirst + tt.running + 1; polls.Load() != want {
				t.Errorf("polls = %d, want %d", polls.Load(), want)
			}
		})
	}
}

func TestWaitBackupRunCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(backupRun{ID: 7, Status: "running"})
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := &Client{server: srv.URL, token: "t", http: srv.Client()}
	if _, err := waitBackupRun(ctx, c, 7, 10*time.Millisecond); err == nil {
		t.Fatal("want the context error")
	}
}

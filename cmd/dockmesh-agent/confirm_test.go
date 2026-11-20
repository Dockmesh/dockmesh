//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

type closeCounter struct{ n atomic.Int32 }

func (c *closeCounter) Close() error { c.n.Add(1); return nil }

func TestConfirmRollbackAsksOnlyForItsOwnUnconfirmedRollback(t *testing.T) {
	unconfirmed := &agents.AgentUpdateResult{Status: string(rootupdate.StatusUnconfirmed), From: "v0.6.0", To: "v0.6.1"}
	tests := []struct {
		name    string
		res     *agents.AgentUpdateResult
		running string
		request bool // a request is already staged
		want    bool
	}{
		{"unconfirmed rollback to this version", unconfirmed, "v0.6.0", false, true},
		{"without v prefix", unconfirmed, "0.6.0", false, true},
		{"no result", nil, "v0.6.0", false, false},
		{"rolled back", &agents.AgentUpdateResult{Status: string(rootupdate.StatusRolledBack), From: "v0.6.0", To: "v0.6.1"}, "v0.6.0", false, false},
		{"failed", &agents.AgentUpdateResult{Status: string(rootupdate.StatusFailed), From: "v0.6.0", To: "v0.6.1"}, "v0.6.0", false, false},
		{"another version runs", unconfirmed, "v0.6.1", false, false},
		{"request already staged", unconfirmed, "v0.6.0", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAgentVersion(t, tt.running)
			data := t.TempDir()
			dir := updateDir(data)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			reqPath := filepath.Join(dir, rootupdate.RequestFile)
			if tt.request {
				if err := os.WriteFile(reqPath, []byte(`{"version":"v0.6.2"}`), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			confirmRollback(data, tt.res)
			b, err := os.ReadFile(reqPath)
			var req rootupdate.Request
			_ = json.Unmarshal(b, &req)
			if got := err == nil && req.Type == rootupdate.RequestConfirmRollback; got != tt.want {
				t.Fatalf("confirm request written = %v, want %v (%s)", got, tt.want, b)
			}
		})
	}
}

// Connected on the old version after an unconfirmed rollback: the agent
// asks root to confirm it and reconnects once root wrote the result.
func TestReconnectForResultConfirmsUnconfirmedRollback(t *testing.T) {
	setAgentVersion(t, "v0.6.0")
	data := t.TempDir()
	dir := updateDir(data)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, rootupdate.PendingFile)
	if err := os.WriteFile(pending, []byte(`{"from":"v0.6.0","to":"v0.6.1"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	sent := &agents.AgentUpdateResult{Status: string(rootupdate.StatusUnconfirmed), From: "v0.6.0", To: "v0.6.1"}
	conn := &closeCounter{}
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		reconnectForResult(ctx, conn, data, sent)
		close(done)
	}()

	reqPath := filepath.Join(dir, rootupdate.RequestFile)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(reqPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no confirm request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// What root does once it saw this version healthy.
	b, _ := json.Marshal(rootupdate.Result{Status: rootupdate.StatusRolledBack, From: "v0.6.0", To: "v0.6.1"})
	if err := os.WriteFile(filepath.Join(dir, rootupdate.ResultFile), b, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(reqPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("reconnectForResult did not return")
	}
	if conn.n.Load() != 1 {
		t.Fatalf("connection closed %d times, want once to report the result", conn.n.Load())
	}
	if res := lastUpdateResult(data); res == nil || res.Status != string(rootupdate.StatusRolledBack) {
		t.Fatalf("result %+v", res)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func resetUninstallState(t *testing.T) {
	t.Helper()
	reset := func() {
		uninstalled.Store(false)
		for _, ch := range []chan struct{}{uninstallRequests, uninstallAcks} {
			select {
			case <-ch:
			default:
			}
		}
	}
	reset()
	t.Cleanup(reset)
}

func TestReportUninstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  bool
	}{
		{"confirmed", true},
		{"no answer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetUninstallState(t)
			old := uninstallAckWait
			uninstallAckWait = 300 * time.Millisecond
			t.Cleanup(func() { uninstallAckWait = old })
			dir := t.TempDir()
			conn, frames := wsPair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				reportUninstall(ctx, conn, dir)
				close(done)
			}()
			requestUninstallReport()
			if f := recvFrame(t, frames); f.Type != agents.FrameAgentUninstalled {
				t.Fatalf("frame %q, want %q", f.Type, agents.FrameAgentUninstalled)
			}
			if tc.ack {
				ackUninstall()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("reportUninstall did not return")
			}
			_, err := os.Stat(filepath.Join(dir, uninstalledFile))
			if (err == nil) != tc.ack || uninstalled.Load() != tc.ack {
				t.Fatalf("confirmation file err=%v, uninstalled=%v; want confirmed=%v", err, uninstalled.Load(), tc.ack)
			}
		})
	}
}

func TestRememberAgentName(t *testing.T) {
	dir := t.TempDir()
	rememberAgentName(dir, "edge-1")
	before, err := os.Stat(filepath.Join(dir, agentNameFile))
	if err != nil {
		t.Fatal(err)
	}
	rememberAgentName(dir, "edge-1")
	rememberAgentName(dir, "")
	after, err := os.Stat(filepath.Join(dir, agentNameFile))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("an unchanged name was written again")
	}
	b, err := readDataFile(dir, agentNameFile, maxAgentName)
	if err != nil || cleanAgentName(b) != "edge-1" {
		t.Fatalf("stored name %q, %v", b, err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readDataFile(dir, "link", 64); err == nil {
		t.Fatal("readDataFile followed a symlink")
	}
	if got := cleanAgentName([]byte("a\x1b[2Jb\n")); got != "a[2Jb" {
		t.Fatalf("cleanAgentName kept control characters: %q", got)
	}
}

func TestReportUninstallDropsStaleRequest(t *testing.T) {
	resetUninstallState(t)
	conn, frames := wsPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestUninstallReport()
	uninstallRequestedAt.Store(time.Now().Add(-2 * uninstallRequestTTL).UnixNano())
	go reportUninstall(ctx, conn, t.TempDir())
	select {
	case f := <-frames:
		t.Fatalf("sent %q for a stale request", f.Type)
	case <-time.After(300 * time.Millisecond):
	}
}

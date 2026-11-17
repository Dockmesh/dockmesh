package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/gorilla/websocket"
)

const (
	// agentNameFile keeps the host's name in Dockmesh for `dockmesh-agent
	// uninstall`, which runs without a connection.
	agentNameFile = "agent.name"
	// uninstalledFile tells `dockmesh-agent uninstall` that the server
	// marked the host as removed.
	uninstalledFile = "uninstalled"
	maxAgentName    = 256
)

// uninstallAckWait bounds the wait for the server's answer to the
// uninstalled frame.
var uninstallAckWait = 10 * time.Second

var (
	// uninstallRequests holds the request of `dockmesh-agent uninstall`
	// (SIGUSR1) until a session can send it.
	uninstallRequests = make(chan struct{}, 1)
	uninstallAcks     = make(chan struct{}, 1)
	// uninstalled keeps the agent from connecting again once the server
	// took the frame; systemd stops it right after.
	uninstalled atomic.Bool
)

func requestUninstallReport() {
	select {
	case uninstallRequests <- struct{}{}:
	default:
	}
}

func ackUninstall() {
	select {
	case uninstallAcks <- struct{}{}:
	default:
	}
}

// reportUninstall sends the uninstalled frame on conn once `dockmesh-agent
// uninstall` asks for it, and on the server's answer writes
// uninstalledFile and ends the session.
func reportUninstall(ctx context.Context, conn *websocket.Conn, dataDir string) {
	select {
	case <-ctx.Done():
		return
	case <-uninstallRequests:
	}
	select {
	case <-uninstallAcks:
	default:
	}
	if err := writeFrame(conn, agents.Frame{Type: agents.FrameAgentUninstalled}); err != nil {
		slog.Warn("uninstall: could not tell the server", "err", err)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(uninstallAckWait):
		slog.Warn("uninstall: the server did not confirm the removal")
		return
	case <-uninstallAcks:
	}
	uninstalled.Store(true)
	if err := writeDataFile(dataDir, uninstalledFile, []byte("removed\n")); err != nil {
		slog.Warn("uninstall: write the confirmation", "err", err)
	}
	slog.Info("the server marked this host as removed from Dockmesh")
	_ = conn.Close()
}

// rememberAgentName stores the host's name when it changed.
func rememberAgentName(dataDir, name string) {
	name = cleanAgentName([]byte(name))
	if name == "" {
		return
	}
	if old, err := readDataFile(dataDir, agentNameFile, maxAgentName); err == nil && cleanAgentName(old) == name {
		return
	}
	if err := writeDataFile(dataDir, agentNameFile, []byte(name+"\n")); err != nil {
		slog.Warn("store the host name", "err", err)
	}
}

// cleanAgentName keeps the printable part of a stored name; root prints
// and compares it.
func cleanAgentName(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxAgentName {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}

func writeDataFile(dataDir, name string, data []byte) error {
	return agents.WriteFileAtomic(dataDir, name, bytes.NewReader(data), 0o600)
}

// readDataFile reads a small file of the data dir without following a
// symlink at its name.
func readDataFile(dataDir, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

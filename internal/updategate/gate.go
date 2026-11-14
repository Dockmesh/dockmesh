// Package updategate holds back the server's background jobs right after
// a binary update until the root updater confirmed it. A rollback puts
// back the database as it was before the update, so nothing may change
// containers or files on the strength of rows the new version wrote
// before the update is known to stay. API and UI run meanwhile.
package updategate

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

// Gate is closed while the root updater's pending marker exists. It is
// read once at start: a server started without the marker never waits.
type Gate struct {
	marker string
	poll   time.Duration
	open   chan struct{}
	once   sync.Once

	// mu is held while a held-back job starts, so Close can wait for it.
	mu     sync.Mutex
	closed bool
}

// New returns the gate for the update directory updateDir.
func New(updateDir string) *Gate {
	g := &Gate{marker: filepath.Join(updateDir, rootupdate.PendingFile), poll: 2 * time.Second, open: make(chan struct{})}
	if !g.markerExists() {
		close(g.open)
	}
	return g
}

// markerExists treats any error other than a missing marker as a marker:
// jobs wait rather than start on a guess.
func (g *Gate) markerExists() bool {
	_, err := os.Lstat(g.marker)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("update marker unreadable, holding background jobs", "err", err)
		return true
	}
	return err == nil
}

// Pending reports whether jobs are still held back.
func (g *Gate) Pending() bool {
	select {
	case <-g.open:
		return false
	default:
		return true
	}
}

// Run calls start right away when the gate is open, else once the update
// is confirmed, unless ctx ended or Close was called first.
func (g *Gate) Run(ctx context.Context, name string, start func()) {
	if !g.Pending() {
		start()
		return
	}
	slog.Info("background job waits for the update to be confirmed", "job", name)
	g.once.Do(func() { go g.watch(ctx) })
	go func() {
		select {
		case <-g.open:
		case <-ctx.Done():
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.closed || ctx.Err() != nil {
			return
		}
		slog.Info("update confirmed, starting background job", "job", name)
		start()
	}()
}

// Close keeps held-back jobs from starting and waits for one that is
// starting. Call it before the jobs are stopped at shutdown.
func (g *Gate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

func (g *Gate) watch(ctx context.Context) {
	for {
		if !g.markerExists() {
			close(g.open)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.poll):
		}
	}
}

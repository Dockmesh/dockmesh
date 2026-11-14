package updategate

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

func TestNormalStartIsNotDelayed(t *testing.T) {
	g := New(t.TempDir())
	ran := false
	g.Run(context.Background(), "job", func() { ran = true })
	if !ran || g.Pending() {
		t.Fatalf("ran %v, pending %v: a start without an update must run jobs at once", ran, g.Pending())
	}
}

func TestJobsWaitForConfirmation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, rootupdate.PendingFile)
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(dir)
	g.poll = 5 * time.Millisecond
	var ran atomic.Int32
	for range 3 {
		g.Run(context.Background(), "job", func() { ran.Add(1) })
	}
	time.Sleep(50 * time.Millisecond)
	if ran.Load() != 0 || !g.Pending() {
		t.Fatalf("%d jobs started before the confirmation", ran.Load())
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ran.Load() != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d of 3 jobs started after the confirmation", ran.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A marker written later, e.g. by the next update, does not close a
	// gate that was read at start.
	_ = os.WriteFile(marker, []byte("{}"), 0o644)
	again := false
	g.Run(context.Background(), "late", func() { again = true })
	if !again {
		t.Fatal("job delayed by a marker that appeared after start")
	}
}

// TestCrashBeforeConfirmation covers an updater that died between the
// swap and the confirmation: the marker stays and jobs stay held, even
// across a restart of the server, until the updater's next run settles it.
func TestCrashBeforeConfirmation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, rootupdate.PendingFile)
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := New(dir)
	g.poll = 5 * time.Millisecond
	var ran atomic.Bool
	g.Run(ctx, "job", func() { ran.Store(true) })
	time.Sleep(30 * time.Millisecond)
	cancel() // server stops
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("job started without a confirmation")
	}

	g = New(dir) // server starts again, marker still there
	g.poll = 5 * time.Millisecond
	g.Run(context.Background(), "job", func() { ran.Store(true) })
	time.Sleep(30 * time.Millisecond)
	if ran.Load() {
		t.Fatal("job started after a restart without a confirmation")
	}
	_ = os.Remove(marker) // the updater's boot run confirmed or rolled back
	deadline := time.Now().Add(5 * time.Second)
	for !ran.Load() {
		if time.Now().After(deadline) {
			t.Fatal("job never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCloseStopsHeldJobs(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, rootupdate.PendingFile)
	_ = os.WriteFile(marker, []byte("{}"), 0o644)
	g := New(dir)
	g.poll = 5 * time.Millisecond
	var ran atomic.Bool
	g.Run(context.Background(), "job", func() { ran.Store(true) })
	g.Close() // shutdown begins
	_ = os.Remove(marker)
	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Fatal("job started after Close")
	}
}

func TestUnreadableMarkerHoldsJobs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "update")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, rootupdate.PendingFile), []byte("{}"), 0o644)
	_ = os.Chmod(sub, 0o000)
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	if g := New(sub); !g.Pending() {
		t.Fatal("unreadable update dir opened the gate")
	}
}

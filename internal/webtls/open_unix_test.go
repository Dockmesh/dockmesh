//go:build unix

package webtls

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLoadPairRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	_, keyFile := writePair(t, dir, "one.example")
	fifo := filepath.Join(dir, "fifo.pem")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := LoadPair(fifo, keyFile)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was loaded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

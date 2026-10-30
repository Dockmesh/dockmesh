//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// TestSMBStressWrites runs many back-to-back and parallel writes against
// the signing-required share. Samba answers slow requests with an unsigned
// STATUS_PENDING interim response first, which only happens under load, so
// a single write rarely shows a client that rejects it.
// DM_IT_SMB_STRESS_N scales the number of writes per worker.
func TestSMBStressWrites(t *testing.T) {
	e := LoadEnv(t)
	perWorker := 25
	if v := os.Getenv("DM_IT_SMB_STRESS_N"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("DM_IT_SMB_STRESS_N=%q: want a positive integer", v)
		}
		perWorker = n
	}
	const workers = 4
	sizes := []int{1 << 10, 4 << 10, 64 << 10, 1 << 20, 3 << 20, 20 << 20}

	host, port := splitAddr(e.SMBAddr)
	tg, err := targets.NewSMB(map[string]any{"host": host, "port": port, "share": e.SMBShare,
		"username": e.SMBUser, "password": e.SMBPass, "path": RunID() + "-smb-stress"})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, sizes[len(sizes)-1])
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		fails []string
		wg    sync.WaitGroup
	)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				size := sizes[(w+i)%len(sizes)]
				path := fmt.Sprintf("w%d/d%d/f%d.bin", w, i%7, i)
				if err := writeOnce(tg, path, payload[:size]); err != nil {
					mu.Lock()
					fails = append(fails, fmt.Sprintf("%s (%d B): %v", path, size, err))
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	t.Logf("%d writes, %d failed", workers*perWorker, len(fails))
	for _, f := range fails {
		t.Error(f)
	}
}

func writeOnce(tg *targets.SMB, path string, data []byte) error {
	w, err := tg.Open(context.Background(), path)
	if err != nil {
		return err
	}
	// Small writes mimic tar headers and tiny files of a many-file volume.
	for off := 0; off < len(data); {
		n := min(len(data)-off, 512+off%(256<<10))
		if _, err := w.Write(data[off : off+n]); err != nil {
			_ = w.Close()
			return fmt.Errorf("write at %d: %w", off, err)
		}
		off += n
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	r, err := tg.Read(context.Background(), path)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	var buf bytes.Buffer
	_, rerr := buf.ReadFrom(r)
	cerr := r.Close()
	if rerr != nil {
		return fmt.Errorf("read back: %w", rerr)
	}
	if cerr != nil {
		return fmt.Errorf("read back close: %w", cerr)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		return fmt.Errorf("read back %d bytes, want %d matching", buf.Len(), len(data))
	}
	return nil
}

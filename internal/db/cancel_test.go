package db

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Clients that give up on a request cancel its context mid-query. A driver
// that lets such a cancel land on a later query leaves the pooled
// connection failing with "interrupted" for good.
func TestConnectionsSurviveCancelledQueries(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE cancel_probe (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if _, err := d.Exec(`INSERT INTO cancel_probe VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					time.Sleep(time.Duration(i%5) * 200 * time.Microsecond)
					cancel()
				}()
				var n int
				_ = d.QueryRowContext(ctx, `SELECT count(*) FROM cancel_probe`).Scan(&n)
				cancel()
			}
		}()
	}
	wg.Wait()

	for i := 0; i < 200; i++ {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM cancel_probe`).Scan(&n); err != nil {
			t.Fatalf("query %d after the cancelled ones: %v", i, err)
		}
	}
}

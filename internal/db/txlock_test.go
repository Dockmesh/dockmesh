package db

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A transaction that reads before it writes has to wait for the write lock
// like a single statement does, instead of failing with SQLITE_BUSY when
// another writer committed after its read.
func TestReadThenWriteTransactionsWhileOtherWritersCommit(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "txlock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE counters (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TABLE noise (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO counters (id, n) VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = d.Exec(`INSERT INTO noise (n) VALUES (?)`, i)
			time.Sleep(time.Millisecond)
		}
	}()

	const workers = 20
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- func() error {
				tx, err := d.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback() }()
				var n int
				if err := tx.QueryRowContext(ctx, `SELECT n FROM counters WHERE id = 1`).Scan(&n); err != nil {
					return err
				}
				time.Sleep(2 * time.Millisecond)
				if _, err := tx.ExecContext(ctx, `UPDATE counters SET n = ? WHERE id = 1`, n+1); err != nil {
					return err
				}
				return tx.Commit()
			}()
		}()
	}
	wg.Wait()
	close(stop)
	writer.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("read-then-write transaction: %v", err)
		}
	}
	var n int
	if err := d.QueryRow(`SELECT n FROM counters WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != workers {
		t.Fatalf("counter = %d, want %d: a transaction worked on a stale read", n, workers)
	}
}

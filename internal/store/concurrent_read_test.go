package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Reads take no lock: they open the archive read-only beside a writer. The archive is in WAL
// mode and every connection has a busy timeout, so a read must never fail while a sync writes,
// holds a transaction open or commits.
func TestReadsNeverErrorBesideAWriter(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "data", "m365crawl.db") // a parent m365crawl creates itself, as on Windows it must
	st, err := Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var mode string
	if err := st.db.QueryRowContext(ctx, `pragma journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (%v), want wal", mode, err)
	}
	now := time.Now().UTC()
	if err := st.RecordRun(ctx, Run{StartedAt: now, FinishedAt: now, Status: "ok", Accounts: []string{"*"}}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var writes atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a sync: transactions held open for a while, then committed
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := st.db.BeginTx(ctx, nil)
			if err != nil {
				t.Error(err)
				return
			}
			for range 200 {
				at := time.Now().UTC()
				if err := recordRun(ctx, tx, Run{StartedAt: at, FinishedAt: at, Status: "ok", Accounts: []string{"*"}}); err != nil {
					_ = tx.Rollback()
					t.Error(err)
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
			if err := tx.Commit(); err != nil {
				t.Error(err)
				return
			}
			writes.Add(1)
		}
	}()

	var reads atomic.Int32
	readErrs := make(chan error, 64)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r, err := OpenReadOnly(ctx, db)
				if err == nil {
					var n int
					_, err = r.LastSuccess(ctx)
					if err == nil {
						err = r.db.QueryRowContext(ctx, `select count(*) from sync_runs`).Scan(&n)
					}
					_ = r.Close()
				}
				if err != nil {
					readErrs <- err
					return
				}
				reads.Add(1)
			}
		}()
	}
	time.Sleep(1500 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Errorf("a read failed beside the writer: %v", err)
	}
	if writes.Load() == 0 || reads.Load() == 0 {
		t.Fatalf("writes %d, reads %d: the test did not overlap them", writes.Load(), reads.Load())
	}
}

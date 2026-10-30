package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkIQWriterOperationBudgetIsSeparateFromAcquisition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "archive.db")
	for _, tc := range []struct {
		budget, want time.Duration
	}{{0, WorkIQWriteBudget}, {45 * time.Second, 45 * time.Second}} {
		must0(WithWorkIQWriterFor(context.Background(), path, tc.budget, func(ctx context.Context, s *Store) error {
			deadline, ok := ctx.Deadline()
			if left := time.Until(deadline); !ok || left <= 2*time.Second || left > tc.want {
				t.Fatalf("operation budget %s, want at most %s and more than the acquisition budget", left, tc.want)
			}
			return s.SetWorkIQEnabled(ctx, true)
		}))
	}
}

func TestWorkIQWriterDoesNotReportACompletedWriteAsADeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "archive.db")
	err := WithWorkIQWriterFor(context.Background(), path, 20*time.Millisecond, func(ctx context.Context, s *Store) error {
		<-ctx.Done() // the operation finished its work; only the clock ran out afterwards
		return nil
	})
	if err != nil {
		t.Fatal("a write that returned success was reported as failed", err)
	}
	err = WithWorkIQWriterFor(context.Background(), path, 20*time.Millisecond, func(ctx context.Context, s *Store) error {
		<-ctx.Done()
		return errors.New("write failed")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("a failed write lost its deadline cause", err)
	}
}

// While SQLite waits for a lock another connection took after the archive opened, a WorkIQ write
// stops when its context is done, with the context's error, rather than waiting out the
// five-second busy timeout. A deadline is what the syncer reports as workiq_archive_busy; a
// cancellation stays a cancellation.
func TestWorkIQWriterStopsWaitingForAnotherConnectionsLockAtItsContext(t *testing.T) {
	path := writableArchivePath(t)
	must0(must(Open(context.Background(), path)).Close())
	raw := must(sql.Open("sqlite", path))
	t.Cleanup(func() { _ = raw.Close() })
	conn := must(raw.Conn(context.Background()))
	t.Cleanup(func() { _ = conn.Close() })
	write := func(ctx context.Context, started func()) (time.Duration, error) {
		var start time.Time
		err := WithWorkIQWriterFor(ctx, path, 300*time.Millisecond, func(ctx context.Context, s *Store) error {
			if _, err := conn.ExecContext(context.Background(), "begin exclusive"); err != nil {
				return err
			}
			defer func() { _, _ = conn.ExecContext(context.Background(), "rollback") }()
			start = time.Now()
			started()
			return s.SetWorkIQEnabled(ctx, true)
		})
		return time.Since(start), err
	}
	if took, err := write(context.Background(), func() {}); !errors.Is(err, context.DeadlineExceeded) || took >= 4*time.Second {
		t.Fatalf("a write blocked by another connection did not stop at its deadline: %v after %s", err, took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if took, err := write(ctx, func() { time.AfterFunc(50*time.Millisecond, cancel) }); !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || took >= 4*time.Second {
		t.Fatalf("a canceled write blocked by another connection did not stop as canceled: %v after %s", err, took)
	}
}

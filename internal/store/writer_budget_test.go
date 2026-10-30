package store

import (
	"context"
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

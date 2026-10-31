package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQPageWriteBudgetGrowsWithThePage(t *testing.T) {
	for bytes, want := range map[int]time.Duration{0: 10 * time.Second, 1 << 20: 12 * time.Second,
		10<<20 + 1: 32 * time.Second, 100 << 20: 60 * time.Second} {
		if got := workIQPageWriteBudget(bytes); got != want {
			t.Fatalf("budget(%d) = %s, want %s", bytes, got, want)
		}
	}
}

func TestWorkIQSlowArchiveWritesRetryThenStopResumably(t *testing.T) {
	oldWriter, oldWait := withWorkIQWriter, workIQWait
	t.Cleanup(func() { withWorkIQWriter, workIQWait = oldWriter, oldWait })
	workIQWait = func(context.Context, time.Duration) error { return nil }
	var budgets []time.Duration
	slow := 1
	withWorkIQWriter = func(_ context.Context, _ string, budget time.Duration, _ func(context.Context, *store.Store) error) error {
		budgets = append(budgets, budget)
		if len(budgets) <= slow {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := writeWorkIQArchiveSized(context.Background(), "archive.db", 10*time.Second, nil); err != nil ||
		len(budgets) != 2 || budgets[1] != 20*time.Second {
		t.Fatal("a slow write was not retried with a larger budget", err, budgets)
	}
	budgets, slow = nil, 10
	err := writeWorkIQArchiveSized(context.Background(), "archive.db", 40*time.Second, nil)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_archive_busy" || !errors.Is(err, context.DeadlineExceeded) ||
		len(budgets) != 3 || budgets[2] != 60*time.Second || !strings.Contains(coded.Fix, "again") {
		t.Fatal("persistently slow writes did not stop as a resumable busy archive", err, budgets)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budgets, slow = nil, 10
	if err := writeWorkIQArchiveSized(ctx, "archive.db", time.Second, nil); !errors.Is(err, context.DeadlineExceeded) || len(budgets) != 1 {
		t.Fatal("a cancelled command retried a slow write", err)
	}
}

func TestWorkIQStoredOutcomeCodeIsThePrintedCode(t *testing.T) {
	for err, want := range map[error]string{
		errors.Join(context.Canceled, workIQFailure("workiq_failed")): "interrupted",
		workIQFailure("workiq_page_limit"):                            "workiq_page_limit",
		errors.New("WorkIQ cursor changed before publication"):        "db_error",
	} {
		if got := workIQOutcomeCode(err); got != want {
			t.Fatalf("%v stored as %q, printed as %q", err, got, want)
		}
	}
}

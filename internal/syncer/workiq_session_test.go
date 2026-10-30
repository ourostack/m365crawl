package syncer

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQRetryPolicyHonorsObservableDeadlinesAndFiniteBudget(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		status, try  int
		remaining    time.Duration
		wait         time.Duration
		retry, later bool
	}{
		{"integer", "8", 429, 0, time.Minute, 8 * time.Second, true, false},
		{"date", now.Add(11 * time.Second).Format(http.TimeFormat), 429, 0, time.Minute, 11 * time.Second, true, false},
		{"past-date", now.Add(-time.Hour).Format(http.TimeFormat), 429, 0, time.Minute, 0, true, false},
		{"first-transient", "", 504, 0, time.Minute, time.Second, true, false},
		{"second-transient", "", 503, 1, time.Minute, 2 * time.Second, true, false},
		{"third-transient", "", 500, 2, time.Minute, 4 * time.Second, true, false},
		{"no-fourth-retry", "", 504, 3, time.Minute, 0, false, true},
		{"permanent", "", 403, 0, time.Minute, 0, false, false},
		{"unobservable-header", "", 429, 0, time.Minute, 0, false, true},
		{"too-late", "61", 429, 0, time.Minute, 0, false, true},
		{"absolute-budget", "601", 429, 0, 20 * time.Minute, 0, false, true},
		{"expired-budget", "", 504, 0, 0, 0, false, true},
		{"invalid-date", "not-a-date", 429, 0, time.Minute, 0, false, true},
		{"negative-seconds", "-1", 429, 0, time.Minute, 0, false, true},
		{"overflow-seconds", "999999999999999999999999", 429, 0, time.Minute, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, retry, err := workIQRetryDelay(tc.status, tc.header, tc.try, now, tc.remaining)
			var coded *errs.Coded
			later := errors.As(err, &coded) && coded.Code == "workiq_retry_later"
			if wait != tc.wait || retry != tc.retry || later != tc.later || err != nil && !later {
				t.Fatal("retry policy retried early, exceeded its finite budget, or invented header availability")
			}
		})
	}
}

func TestWorkIQWaitIsCancelableAndShortWriterReleasesBeforeWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWorkIQ(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("retry wait ignored cancellation")
	}
	if err := waitWorkIQ(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	db, _ := workIQAcquisitionArchive(t)
	release, err := store.AcquireWriter(context.Background(), db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	originalWait := workIQWait
	t.Cleanup(func() { workIQWait = originalWait })
	workIQWait = func(context.Context, time.Duration) error {
		unlock, err := store.AcquireLock(db + ".short-writer")
		if err != nil {
			t.Fatal("server retained writer intent during backoff", err)
		}
		unlock()
		release()
		release = func() {}
		return nil
	}
	if err := workIQWrite(context.Background(), db, func(ctx context.Context, s *store.Store) error {
		return s.SetWorkIQEnabled(ctx, false)
	}); err != nil {
		t.Fatal("short writer did not retry after its ordinary owner released", err)
	}
}

func TestWorkIQWriterBackoffRemainsFiniteAndPreservesWaitFailure(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	release, err := store.AcquireWriter(context.Background(), db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	originalNow, originalWait := workIQNow, workIQWait
	t.Cleanup(func() { workIQNow, workIQWait = originalNow, originalWait })
	calls := 0
	base := time.Now()
	workIQNow = func() time.Time {
		calls++
		if calls > 1 {
			return base.Add(11 * time.Minute)
		}
		return base
	}
	err = workIQWrite(context.Background(), db, func(context.Context, *store.Store) error {
		t.Fatal("operation ran without archive ownership")
		return nil
	})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "locked" {
		t.Fatal("bounded writer wait lost its original lock outcome")
	}
	workIQNow = originalNow
	waitFailure := errors.New("synthetic wait failure")
	workIQWait = func(context.Context, time.Duration) error { return waitFailure }
	if err := workIQWrite(context.Background(), db, func(context.Context, *store.Store) error { return nil }); !errors.Is(err, waitFailure) {
		t.Fatal("server swallowed a canceled/failed backoff")
	}
}

func TestWorkIQDisabledAndInvalidAcquisitionDoNotStartClient(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent-synthetic-helper"))
	_, err = RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_disabled" {
		t.Fatal("disabled source tried executable lookup or source acquisition", err)
	}
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{}); err == nil {
		t.Fatal("acquisition used a default real archive")
	}
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: filepath.Join(t.TempDir(), "absent.db"), Source: "mail"}); err == nil {
		t.Fatal("a missing archive became an invented account or empty success")
	}
}

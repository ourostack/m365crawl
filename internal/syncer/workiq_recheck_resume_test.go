package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func workIQPageLimitCode(err error) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == "workiq_page_limit"
}

// A recorded removal owes two complete account censuses. When that rebuild needs more pages than one
// run may fetch, later runs must continue it instead of retiring its cursors again.
func TestWorkIQMailRecheckLargerThanTheCapContinuesAcrossRuns(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	old := workIQMailPageLimit
	t.Cleanup(func() { workIQMailPageLimit = old })
	// An incremental run fetches 16 pages; a full rebuild fetches 21 (the Inbox delta spans six).
	workIQMailPageLimit = 18
	t.Setenv("WORKIQ_ACQUISITION_REMOVE_ONE", "1")
	report, err := RunWorkIQ(context.Background(), options)
	if !workIQPageLimitCode(err) || !report.Resumable || report.Counts["census_rechecks"] == 0 {
		t.Fatalf("the removal's recheck did not start and stop resumably: %v %+v", err, report)
	}
	retired := count(t, db, `select count(*) from workiq_frontiers where retired=1`)
	report, err = RunWorkIQ(context.Background(), options)
	if !workIQPageLimitCode(err) || !report.Resumable {
		t.Fatalf("second run: %v %+v", err, report)
	}
	if count(t, db, `select count(*) from workiq_frontiers where retired=1`) != retired {
		t.Fatal("the next run retired the recheck's cursors again instead of continuing it")
	}
	rechecks := 0
	for run := 3; ; run++ {
		if run > 8 {
			t.Fatal("the owed recheck never finished across runs")
		}
		report, err = RunWorkIQ(context.Background(), options)
		rechecks += report.Counts["census_rechecks"]
		if err == nil {
			break
		}
		if !workIQPageLimitCode(err) || !report.Resumable {
			t.Fatalf("run %d: %v %+v", run, err, report)
		}
	}
	if report.Accounts[0].Status != "ok" || report.Gaps["workiq_mail_census_pending"] != 0 || rechecks == 0 ||
		count(t, db, `select count(*) from mail_messages`) != 6 {
		t.Fatalf("the finished recheck was not counted as complete: %+v", report)
	}
}

// A baseline restart after an expired delta keeps its position when it then reaches the page budget.
func TestWorkIQExpiredRestartThenPageLimitIsResumable(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	old := workIQMailPageLimit
	t.Cleanup(func() { workIQMailPageLimit = old })
	workIQMailPageLimit = 20
	t.Setenv("WORKIQ_ACQUISITION_EXPIRE_ONCE", filepath.Join(t.TempDir(), "expired-once"))
	report, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_page_limit" || !report.Resumable || report.Counts["baseline_restarts"] != 1 {
		t.Fatalf("expired restart then page budget: %v %+v", err, report)
	}
	for run := 2; ; run++ {
		if run > 5 {
			t.Fatal("the restarted baseline never finished")
		}
		if report, err = RunWorkIQ(context.Background(), options); err == nil {
			break
		}
		if !workIQPageLimitCode(err) || !report.Resumable {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if count(t, db, `select count(*) from mail_messages`) != 6 {
		t.Fatal("the restarted baseline lost or duplicated mail")
	}
}

// When the restarted baseline after an expired delta fails for a reason that cannot resume, the
// command reports that run's own outcome, the code its latest attempt stored, not the expiry.
func TestWorkIQExpiredRestartFailureReportsItsStoredCode(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	old := workIQClose
	t.Cleanup(func() { workIQClose = old })
	broken := errors.New("synthetic cleanup failure")
	workIQClose = func(c *workiq.Client) error {
		err := old(c)
		if count(t, db, `select count(*) from workiq_attempts where error_code='workiq_delta_expired'`) == 1 {
			return broken
		}
		return err
	}
	t.Setenv("WORKIQ_ACQUISITION_EXPIRE_ONCE", filepath.Join(t.TempDir(), "expired-once"))
	report, err := RunWorkIQ(context.Background(), options)
	var stored string
	if qerr := openRaw(t, db).QueryRow(`select error_code from workiq_attempts order by id desc limit 1`).Scan(&stored); qerr != nil {
		t.Fatal(qerr)
	}
	if !errors.Is(err, broken) || report.Resumable || stored == "workiq_delta_expired" || workIQOutcomeCode(err) != stored {
		t.Fatalf("restart failure printed %q but stored %q: %v", workIQOutcomeCode(err), stored, err)
	}
}

// An owed recheck's rebuild continues after any stop that left its cursors on their first pass: a
// transient failure, a busy archive or a failed start between runs, not only a page-budget stop.
func TestWorkIQMailRecheckContinuesAfterEveryResumableStopAndFailedStart(t *testing.T) {
	for _, stop := range []string{"workiq_retry_later", "workiq_archive_busy", "failed-start"} {
		t.Run(stop, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
			t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
			options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
			if _, err := RunWorkIQ(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			oldLimit, oldWait, oldWriter := workIQMailPageLimit, workIQWait, withWorkIQWriter
			t.Cleanup(func() { workIQMailPageLimit, workIQWait, withWorkIQWriter = oldLimit, oldWait, oldWriter })
			workIQMailPageLimit = 18
			workIQWait = func(context.Context, time.Duration) error { return nil }
			t.Setenv("WORKIQ_ACQUISITION_REMOVE_ONE", "1")
			if _, err := RunWorkIQ(context.Background(), options); !workIQPageLimitCode(err) {
				t.Fatal("the recheck did not start and stop at the page budget", err)
			}
			retired := count(t, db, `select count(*) from workiq_frontiers where retired=1`)
			switch stop {
			case "workiq_retry_later":
				t.Setenv("WORKIQ_ACQUISITION_MODE", "transient-always")
			case "workiq_archive_busy":
				withWorkIQWriter = func(ctx context.Context, path string, budget time.Duration, op func(context.Context, *store.Store) error) error {
					if _, page := ctx.Value(workIQPageBytesKey{}).(int); page {
						return context.DeadlineExceeded
					}
					return oldWriter(ctx, path, budget, op)
				}
			default:
				t.Setenv("WORKIQ_ACQUISITION_ME", "fail")
			}
			report, err := RunWorkIQ(context.Background(), options)
			var coded *errs.Coded
			if stop != "failed-start" && (!errors.As(err, &coded) || coded.Code != stop || !report.Resumable) {
				t.Fatalf("the interrupting run did not stop resumably with %s: %v %+v", stop, err, report)
			}
			if stop == "failed-start" && (err == nil || report.Resumable) {
				t.Fatal("the failed start succeeded or claimed to be resumable", err)
			}
			t.Setenv("WORKIQ_ACQUISITION_MODE", "")
			t.Setenv("WORKIQ_ACQUISITION_ME", "")
			withWorkIQWriter = oldWriter
			for run := 0; ; run++ {
				if run > 6 {
					t.Fatal("the owed recheck never finished")
				}
				report, err = RunWorkIQ(context.Background(), options)
				if err == nil {
					break
				}
				if !workIQPageLimitCode(err) || !report.Resumable {
					t.Fatalf("run %d: %v %+v", run, err, report)
				}
			}
			// Two owed checks retire the account's cursors exactly twice; restarting the interrupted
			// rebuild would have retired them a third time.
			if got := count(t, db, `select count(*) from workiq_frontiers where retired=1`); got != 2*retired {
				t.Fatalf("after %s the rebuild was restarted instead of continued: %d retired cursors, want %d", stop, got, 2*retired)
			}
			if count(t, db, `select count(*) from mail_messages`) != 6 {
				t.Fatal("the continued recheck lost mail")
			}
		})
	}
}

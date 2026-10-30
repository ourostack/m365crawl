package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
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

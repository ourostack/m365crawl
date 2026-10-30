package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Live: one page failed four times in about 33 s and the same cursor then returned 200 twice.
// Exhausted transient failures keep the cursor, so the same command run again continues.
func TestWorkIQExhaustedTransientFailuresAreRetryLaterAndResumable(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWait
	t.Cleanup(func() { workIQWait = original })
	workIQWait = func(context.Context, time.Duration) error { return nil }
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	t.Setenv("WORKIQ_ACQUISITION_MODE", "transient-always")
	report, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_retry_later" || !report.Resumable ||
		!strings.Contains(coded.Fix, "run the same workiq command again") ||
		count(t, db, `select count(*) from workiq_attempts where error_code='workiq_retry_later'`) != 1 {
		t.Fatalf("exhausted transient failures: %v %+v", err, report)
	}
	t.Setenv("WORKIQ_ACQUISITION_MODE", "")
	if _, err := RunWorkIQ(context.Background(), options); err != nil || count(t, db, `select count(*) from mail_messages`) != 1 {
		t.Fatal("the next run did not continue after transient failures", err)
	}
}

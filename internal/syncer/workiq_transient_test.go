package syncer

import (
	"context"
	"errors"
	"path/filepath"
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

// Live: a run ended `workiq_failed` after 840 pages with nothing to explain it. A WorkIQ child that
// ends mid-session is reopened and the same page retried; the run continues from there.
func TestWorkIQSessionLostMidRunIsReopenedAndTheSamePageRetried(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWait
	t.Cleanup(func() { workIQWait = original })
	workIQWait = func(context.Context, time.Duration) error { return nil }
	t.Setenv("WORKIQ_ACQUISITION_DIE_ONCE", filepath.Join(t.TempDir(), "died"))
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	if err != nil || count(t, db, `select count(*) from mail_messages`) != 1 || report.Accounts[0].Diagnostic != nil {
		t.Fatalf("a lost session was not reopened and retried: %v %+v", err, report)
	}
}

// When the child keeps ending, the run stops resumably with its position kept, and the result and
// the archive keep a redacted diagnostic of what the child left behind.
func TestWorkIQSessionLostEveryTimeIsRetryLaterWithADiagnostic(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWait
	t.Cleanup(func() { workIQWait = original })
	workIQWait = func(context.Context, time.Duration) error { return nil }
	t.Setenv("WORKIQ_ACQUISITION_DIE_ALWAYS", "1")
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	report, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_retry_later" || !report.Resumable || len(report.Accounts) != 1 {
		t.Fatalf("repeated lost sessions: %v %+v", err, report)
	}
	d := report.Accounts[0].Diagnostic
	if d == nil || d.Exit != "exit status 3" || d.Stderr != "fatal: transport connection lost for [redacted]" {
		t.Fatalf("diagnostic = %+v", d)
	}
	if count(t, db, `select count(*) from meta where key like 'workiq_diagnostic:%' and json_extract(value,'$.exit')='exit status 3'`) != 1 {
		t.Fatal("the attempt did not keep the diagnostic")
	}
	t.Setenv("WORKIQ_ACQUISITION_DIE_ALWAYS", "")
	if _, err := RunWorkIQ(context.Background(), options); err != nil || count(t, db, `select count(*) from mail_messages`) != 1 {
		t.Fatal("the next run did not continue after lost sessions", err)
	}
}

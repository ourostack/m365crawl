package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQMailAcquisitionRefusalsNeverPublishSourceSuccess(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"me-failed", "WORKIQ_ACQUISITION_ME", "fail"},
		{"me-invalid", "WORKIQ_ACQUISITION_ME", "missing-id"},
		{"pin-mismatch", "WORKIQ_ACQUISITION_ME", "pinned-mismatch"},
		{"permanent", "WORKIQ_ACQUISITION_MODE", "permanent"},
		{"child-failed", "WORKIQ_ACQUISITION_MODE", "child-fail"},
		{"duplicate-child", "WORKIQ_ACQUISITION_MODE", "duplicate-child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			t.Setenv(tc.key, tc.value)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
			if err == nil || count(t, db, `select count(*) from mail_messages`) != 0 ||
				count(t, db, `select count(*) from sync_runs where source like 'workiq:%' and status in ('ok','ok_with_omissions')`) != 0 {
				t.Fatal("refused/incomplete source admitted mail or covering success")
			}
			for _, account := range report.Accounts {
				if account.Status != "failed" {
					t.Fatal("a refused source reported a positive outcome")
				}
			}
		})
	}
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail"}); err == nil {
		t.Fatal("a no-identity account bootstrapped without explicit archive selection")
	}
}

func TestWorkIQMailAcquisitionLargeMissingIDPageRemainsBoundedAndSeparate(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "200")
	t.Setenv("WORKIQ_ACQUISITION_MODE", "no-message-id")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	if err != nil {
		t.Fatal(err)
	}
	if count(t, db, `select count(*) from mail_messages`) != 200 ||
		report.Counts["mail_added"] != 200 || report.Gaps["workiq_mail_message_id_missing"] != 200 ||
		len(report.Accounts) != 1 || report.Accounts[0].Status != "ok_with_omissions" {
		t.Fatal("bounded missing-ID groups merged copies, lost counts, or claimed complete coverage")
	}
	if count(t, db, `select count(*) from workiq_attempts where all_folders=1`) != 0 {
		t.Fatal("a missing-identity gap produced whole-account coverage")
	}
}

func TestWorkIQMailAcquisitionIncrementalCarryDoesNotFabricateNewSightings(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "200")
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := openRaw(t, db).QueryRow(`select group_concat(server_seen_at,'|') from workiq_rows where kind='mail' order by item_rowid`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	report, err := RunWorkIQ(context.Background(), options)
	if err != nil {
		t.Fatal("incremental retained inventory failed", err)
	}
	if err := openRaw(t, db).QueryRow(`select group_concat(server_seen_at,'|') from workiq_rows where kind='mail' order by item_rowid`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if report.Counts["mail_added"] != 0 || count(t, db, `select count(*) from mail_messages`) != 200 || after != before {
		t.Fatal("empty delta dropped rows, duplicated them, or advanced their original source observations")
	}
}

func TestWorkIQMailAcquisitionReducesIdempotentPreparationAfterShortBudget(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWrite
	t.Cleanup(func() { workIQWrite = original })
	calls := 0
	workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
		calls++
		if calls == 4 { // first stored fixed-folder preparation, not attempt creation
			return context.DeadlineExceeded
		}
		return original(ctx, path, op)
	}
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account}); err != nil {
		t.Fatal("idempotent page preparation did not retry a smaller bounded chunk", err)
	}
	if count(t, db, `select count(*) from mail_messages`) != 1 {
		t.Fatal("short-budget preparation lost or duplicated data")
	}
}

func TestWorkIQMailTransientExhaustionAndCanceledBackoffRemainFailed(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "canceled"}[canceled], func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			t.Setenv("WORKIQ_ACQUISITION_MODE", "transient-always")
			original := workIQWait
			t.Cleanup(func() { workIQWait = original })
			waits := 0
			workIQWait = func(context.Context, time.Duration) error {
				waits++
				if canceled {
					return context.Canceled
				}
				return nil
			}
			_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
			if err == nil || !canceled && waits != 3 || canceled && !errors.Is(err, context.Canceled) ||
				count(t, db, `select count(*) from mail_messages`) != 0 {
				t.Fatal("transient exhaustion/cancellation published data or retried without a bound")
			}
		})
	}
}

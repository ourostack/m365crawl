package syncer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQExpiredDeltaStartsVerifiedBaselineAndRetainsGoodCursor(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var oldRow int64
	if err := openRaw(t, db).QueryRow(`select rowid from mail_messages`).Scan(&oldRow); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_EXPIRE_ONCE", filepath.Join(t.TempDir(), "expired-once"))
	report, err := RunWorkIQ(context.Background(), options)
	if err != nil {
		t.Fatal("an expired good cursor did not recover through a fresh verified baseline", err)
	}
	if report.Counts["baseline_restarts"] != 1 || len(report.Gaps) != 0 ||
		count(t, db, `select count(*) from mail_messages`) != 1 ||
		count(t, db, `select rowid from mail_messages`) != int(oldRow) {
		t.Fatal("baseline recovery duplicated/reidentified mail or left a falsely resolved/current gap")
	}
	if count(t, db, `select count(*) from workiq_frontiers
where resource='/me/mailFolders/folder-inbox/messages/delta()' and retired=1 and cursor like '%stable%2bstate'`) != 1 ||
		count(t, db, `select count(*) from workiq_frontiers
where resource='/me/mailFolders/folder-inbox/messages/delta()' and retired=0 and generation=1 and admitted_revision>0`) != 1 {
		t.Fatal("expired baseline overwrote the good cursor or failed to admit a new generation")
	}
	if count(t, db, `select count(*) from workiq_attempts where status='failed' and error_code='workiq_delta_expired'`) != 1 {
		t.Fatal("baseline recovery erased the original failed enumeration")
	}
}

func TestWorkIQExpiredDeltaCannotRestartSameResourceTwice(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	resource := "/me/mailFolders/folder-inbox/messages/delta()"
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account, restarted: map[string]bool{resource: true}}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_EXPIRE_ONCE", filepath.Join(t.TempDir(), "expired-once"))
	if _, err := RunWorkIQ(context.Background(), options); err == nil ||
		count(t, db, `select count(*) from workiq_frontiers where retired=1`) != 0 ||
		count(t, db, `select count(*) from mail_messages`) != 1 {
		t.Fatal("a repeated expired resource caused an unbounded restart or changed retained mail")
	}
}

func TestWorkIQExpiredDeltaRecoveryIsBoundedPrivateAndCancelable(t *testing.T) {
	expired := &workIQExpired{resource: "/me/mailFolders/private-opaque-id/messages/delta()", revision: 1}
	if text := fmt.Sprintf("%v|%#v", expired, expired); strings.Contains(text, "private-opaque") ||
		!strings.Contains(text, "workiq_delta_expired") {
		t.Fatal("an expired frontier exposed its private resource in diagnostics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := retryWorkIQBaseline(ctx, WorkIQOptions{}, WorkIQReport{}, expired); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled baseline recovery performed source work")
	}
	restarted := map[string]bool{}
	for i := 0; i < 16; i++ {
		restarted[fmt.Sprint(i)] = true
	}
	if _, err := retryWorkIQBaseline(context.Background(), WorkIQOptions{restarted: restarted}, WorkIQReport{}, expired); err == nil {
		t.Fatal("baseline recovery exceeded the shared command restart bound")
	}
	options := WorkIQOptions{DBPath: filepath.Join(t.TempDir(), "absent.db"), Source: "mail"}
	if _, err := retryWorkIQBaseline(context.Background(), options, WorkIQReport{}, expired); err == nil {
		t.Fatal("an unavailable selected archive became a successful restarted baseline")
	}
}

func TestWorkIQMailIdentityChangeAfterPinRefusesAttemptAdmission(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWrite
	t.Cleanup(func() { workIQWrite = original })
	first := true
	workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
		if first {
			first = false
			if _, err := openRaw(t, db).Exec(`insert into meta(key,value) values(?,?)`,
				"outlook_identity:"+account, "different@example.invalid"); err != nil {
				t.Fatal(err)
			}
		}
		return original(ctx, path, op)
	}
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_identity_mismatch" ||
		count(t, db, `select count(*) from mail_messages`) != 0 ||
		count(t, db, `select count(*) from sync_runs where source like 'workiq:%' and status='failed'`) != 1 {
		t.Fatal("changed archive identity was admitted, lost its original failure, or invented source data")
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func beginRealFullWorkIQCensus(t *testing.T, s *Store, previous int64, values []json.RawMessage) int64 {
	t.Helper()
	ctx := context.Background()
	resource := "/me/mailFolders/inbox-id/messages/delta"
	var attempt int64
	if previous == 0 {
		attempt = seedWorkIQMailAttempt(t, s)
	} else {
		for _, path := range []string{resource, "/me/mailFolders?includeHiddenFolders=true"} {
			p := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, path))
			must0(s.AdmitWorkIQFrontier(ctx, previous, path, p.Revision))
		}
		must0(s.CompleteWorkIQAttempt(ctx, previous, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
		attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
		verifyWorkIQTestAttempt(t, s, attempt)
		p := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
		must0(s.RestartWorkIQFrontier(ctx, attempt, resource, p.Revision))
	}
	prepareRealMailInventory(t, s, attempt, values)
	return attempt
}

func TestWorkIQMailCensusBatchPreflightsThenMarksOnlyBoundedRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test"), graphMailFixture("two", "inbox-id", "two@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "two@example.test"))
	run := beginRealFullWorkIQCensus(t, s, first, []json.RawMessage{})
	for i := 0; i < 3; i++ {
		done, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 1)
		must0(err)
		if done || rowCount(t, s, `select count(*) from workiq_rows where server_first_missing_census is not null`) != 0 {
			t.Fatal("census wrote absence before all retained identities were checked")
		}
	}
	done, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 1)
	must0(err)
	if done || rowCount(t, s, `select count(*) from workiq_rows where server_first_missing_census is not null`) != 1 {
		t.Fatal("bounded census did not mark exactly its first canonical row")
	}
	for i := 0; i < 4 && !done; i++ {
		done, err = s.ConfirmWorkIQMailCensusBatch(ctx, run, 1)
		must0(err)
	}
	if !done || rowCount(t, s, `select count(*) from mail_messages where gone_at is not null`) != 0 {
		t.Fatal("first full census confirmed absence or failed to finish")
	}
	next := beginRealFullWorkIQCensus(t, s, run, []json.RawMessage{})
	for i := 0; i < 8; i++ {
		done, err = s.ConfirmWorkIQMailCensusBatch(ctx, next, 1)
		must0(err)
		if done {
			break
		}
	}
	if !done || rowCount(t, s, `select count(*) from mail_messages where gone_at is not null`) != 2 {
		t.Fatal("second distinct gap-free full census did not confirm both source-only absences")
	}
	before := dump(t, s)
	if done, err := s.ConfirmWorkIQMailCensusBatch(ctx, next, 1); err != nil || !done || dump(t, s) != before {
		t.Fatal("completed census replay changed source or canonical clocks")
	}
}

func TestWorkIQMailCensusBatchUnknownIdentityStopsBeforeAnyMark(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test"), graphMailFixture("unknown", "inbox-id", "")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, first, ""))
	run := beginRealFullWorkIQCensus(t, s, first, []json.RawMessage{})
	if done, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 1); err != nil || done {
		t.Fatal("first bounded identity preflight failed")
	}
	before := dump(t, s)
	if _, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 1); err == nil || dump(t, s) != before {
		t.Fatal("unknown retained identity was accepted or partially marked")
	}
	if rowCount(t, s, `select count(*) from workiq_rows where server_first_missing_census is not null or server_gone_at is not null`) != 0 {
		t.Fatal("late unknown identity allowed earlier absence marks")
	}
}

func finishWorkIQCensusBatches(t *testing.T, s *Store, attempt int64) {
	t.Helper()
	for i := 0; i < 16; i++ {
		done, err := s.ConfirmWorkIQMailCensusBatch(context.Background(), attempt, 128)
		must0(err)
		if done {
			return
		}
	}
	t.Fatal("small bounded census did not finish")
}

func setupWorkIQBatchCensus(t *testing.T, s *Store, mode string) int64 {
	t.Helper()
	ctx := context.Background()
	first := beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test"), graphMailFixture("two", "inbox-id", "two@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "two@example.test"))
	run := beginRealFullWorkIQCensus(t, s, first, []json.RawMessage{})
	if mode == "second-mark" {
		finishWorkIQCensusBatches(t, s, run)
		run = beginRealFullWorkIQCensus(t, s, run, []json.RawMessage{})
	}
	if mode != "preflight" {
		if done, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 128); err != nil || done {
			t.Fatal("actual preflight did not prepare a bounded mark transaction")
		}
	}
	return run
}

func TestWorkIQMailCensusBatchFaultsRollbackProgressAndMarks(t *testing.T) {
	for _, mode := range []string{"preflight", "first-mark", "second-mark"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			setup := func(t *testing.T, s *Store) { attempt = setupWorkIQBatchCensus(t, s, mode) }
			op := func(ctx context.Context, s *Store) error {
				_, err := s.ConfirmWorkIQMailCensusBatch(ctx, attempt, 128)
				return err
			}
			sweepFaults(t, setup, op)
			s := newStore(t)
			setup(t, s)
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, "rowsaffected", true, op, nil)
		})
	}
}

func TestWorkIQMailCensusBatchRejectsMalformedScopeProofAndState(t *testing.T) {
	for _, edit := range []string{
		`update workiq_censuses set complete=0`,
		`update workiq_censuses set full_enumeration=0`,
		`update workiq_censuses set completed_at=null`,
		`update workiq_censuses set completed_at='bad'`,
		`update workiq_rows set server_projection='{'`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.ID','foreign')`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.InternetMessageID',null)`,
		`update workiq_rows set server_seen_at='bad'`,
		`update workiq_rows set server_gone_at='bad'`,
		`update workiq_rows set server_deleted_at='bad'`,
		`update workiq_rows set server_version='bad'`,
		`update workiq_rows set server_deleted_version='bad'`,
		`create trigger block_census before update on workiq_censuses begin select raise(ignore); end`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := setupWorkIQBatchCensus(t, s, "preflight")
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.ConfirmWorkIQMailCensusBatch(ctx, attempt, 128); err == nil || dump(t, s) != before {
				t.Fatal("invalid census/source evidence or lost progress CAS was accepted")
			}
		})
	}
	s := newStore(t)
	attempt := setupWorkIQBatchCensus(t, s, "preflight")
	ctx := context.Background()
	for _, limit := range []int{0, 129} {
		if _, err := s.ConfirmWorkIQMailCensusBatch(ctx, attempt, limit); err == nil {
			t.Fatal("unbounded census write accepted")
		}
	}
	chat := seedWorkIQConversation(t, s)
	for _, bad := range []int64{-1, chat} {
		if _, err := s.ConfirmWorkIQMailCensusBatch(ctx, bad, 128); err == nil {
			t.Fatal("unverified/non-mail census confirmation accepted")
		}
	}
}

func TestWorkIQMailCensusBatchEligibilityStatesKeepIndependentHistory(t *testing.T) {
	for _, mode := range []string{"present", "deleted", "newer-live", "repeated", "already-gone", "restored-delete"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			var run int64
			if mode == "present" {
				run = beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
				_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
			} else {
				run = setupWorkIQBatchCensus(t, s, "preflight")
			}
			var edit string
			switch mode {
			case "deleted":
				edit = `update workiq_rows set server_deleted_at='2026-09-10T07:00:00Z',server_deleted_version='2026-09-10T08:01:00Z'`
			case "newer-live":
				edit = `update workiq_rows set server_seen_at='2026-09-10T09:00:00Z'`
			case "restored-delete":
				edit = `update workiq_rows set server_deleted_at='2026-09-10T07:00:00Z',server_deleted_version='2026-09-10T07:00:00Z'`
			case "repeated", "already-gone":
				must0(s.ConfirmWorkIQMailCensus(ctx, run))
				if mode == "already-gone" {
					edit = `update workiq_rows set server_gone_at='2026-09-10T06:00:00Z'`
				}
			}
			if edit != "" {
				_, err := s.db.ExecContext(ctx, edit)
				must0(err)
			}
			finishWorkIQCensusBatches(t, s, run)
			if mode == "present" || mode == "deleted" || mode == "newer-live" {
				if rowCount(t, s, `select count(*) from workiq_rows where server_first_missing_census is not null`) != 0 {
					t.Fatal("present/newer/deleted source was absence-marked")
				}
			}
			if mode == "already-gone" && rowCount(t, s, `select count(*) from workiq_rows where server_gone_at='2026-09-10T06:00:00Z'`) != 2 {
				t.Fatal("census replay refreshed an already confirmed source mark")
			}
			if mode == "restored-delete" {
				next := beginRealFullWorkIQCensus(t, s, run, []json.RawMessage{})
				finishWorkIQCensusBatches(t, s, next)
				if rowCount(t, s, `select count(*) from mail_messages where gone_at is not null and deleted_at is null`) != 2 ||
					rowCount(t, s, `select count(*) from workiq_rows where server_deleted_at is not null`) != 2 {
					t.Fatal("historical overridden deletion blocked later census absence or lost its own history")
				}
			}
		})
	}
}

func TestWorkIQMailCensusBatchRejectsNewGapAndInconsistentPriorProof(t *testing.T) {
	for _, mode := range []string{"gap", "order", "previous"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			setupMode := "preflight"
			if mode == "previous" {
				setupMode = "second-mark"
			}
			run := setupWorkIQBatchCensus(t, s, setupMode)
			switch mode {
			case "gap":
				_, err := s.db.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,n) values(?,'workiq_policy_denied',1)`, run)
				must0(err)
			case "order":
				_, err := s.db.ExecContext(ctx, `update workiq_rows set server_first_missing_census=(select id+1 from workiq_censuses where attempt_id=?)`, run)
				must0(err)
				_, err = s.ConfirmWorkIQMailCensusBatch(ctx, run, 128)
				must0(err)
			case "previous":
				_, err := s.db.ExecContext(ctx, `update workiq_censuses set full_enumeration=0 where id in(select server_first_missing_census from workiq_rows)`)
				must0(err)
			}
			before := dump(t, s)
			if _, err := s.ConfirmWorkIQMailCensusBatch(ctx, run, 128); err == nil || dump(t, s) != before {
				t.Fatal("new gap or inconsistent prior census proof was accepted or partially marked")
			}
		})
	}
}

func TestWorkIQMailCensusBatchRehydratedPagesAreNotASecondEnumeration(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := beginRealFullWorkIQCensus(t, s, 0, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	run := beginRealFullWorkIQCensus(t, s, first, []json.RawMessage{})
	finishWorkIQCensusBatches(t, s, run)
	must0(s.CompleteWorkIQAttempt(ctx, run, WorkIQSummary{Status: "partial", FinishedAt: mailT0}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	for _, path := range []string{"/me/mailFolders?includeHiddenFolders=true", "/me/mailFolders/inbox-id/messages/delta"} {
		var id int64
		must0(s.db.QueryRowContext(ctx, `select p.id from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where f.resource=? and f.retired=0 and p.state='staged' order by p.expected_revision`, path).Scan(&id))
		if done, err := s.PrepareWorkIQMailPage(ctx, next, id, 128); err != nil || !done {
			t.Fatal("original pending page did not rehydrate under the new verified owner")
		}
	}
	must0(s.SealWorkIQMailInventory(ctx, next, mailT0.Add(time.Hour)))
	must0(s.ConfirmWorkIQMailCensus(ctx, next))
	finishWorkIQCensusBatches(t, s, next)
	if rowCount(t, s, `select count(*) from mail_messages where gone_at is not null`) != 0 {
		t.Fatal("replaying the same stored pages under a new attempt counted as a second whole enumeration")
	}
	var dataAt string
	must0(s.db.QueryRowContext(ctx, `select source_data_at from workiq_censuses where attempt_id=?`, next).Scan(&dataAt))
	original := must(workIQStoredTime(sql.NullString{String: dataAt, Valid: true}))
	if original == nil || !original.Equal(mailT0) {
		t.Fatal("rehydration refreshed the original all-folder source-data clock")
	}
}

func TestWorkIQMailEnumerationProofRejectsUnknownOriginalClock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{}})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
		t.Fatal("empty actual root did not prepare")
	}
	_, err = s.db.ExecContext(ctx, `update workiq_pages set observed_at='0001-01-01T00:00:00Z' where id=?`, id)
	must0(err)
	before := dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("unknown original page clock qualified a complete inventory")
	}
	_, err = s.db.ExecContext(ctx, `update workiq_pages set observed_at='bad' where id=?`, id)
	must0(err)
	before = dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("corrupt original clock became an inventory completion")
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		_, _, err := workIQMailEnumerationProof(ctx, tx, attempt, WorkIQScope{Family: "mail", Account: mailAcct, Qualification: "different"}, 0, false)
		return err
	})
	if err == nil || dump(t, s) != before {
		t.Fatal("unmatched original enumeration scope produced a receipt proof")
	}
}

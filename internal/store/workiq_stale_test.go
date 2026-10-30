package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkIQCloseStaleAttemptsSettlesOnlyOpenAttemptsOfItsScope(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	stale := seedWorkIQMailAttempt(t, s)
	probe := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "coverage"}, mailT0))
	// Another account's and another family's open attempts of the same kind may have live owners.
	other, err := s.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,kind) values('mail','outlook/other',?,'sync')`, mailT0.Format(time.RFC3339Nano))
	must0(err)
	otherID := must(other.LastInsertId())
	chats, err := s.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,kind) values('chats',?,?,'sync')`, mailAcct, mailT0.Format(time.RFC3339Nano))
	must0(err)
	chatsID := must(chats.LastInsertId())
	done := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	must0(s.CompleteWorkIQAttempt(ctx, done, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0}))
	at := mailT0.Add(time.Hour)
	if n := must(s.CloseStaleWorkIQAttempts(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "sync"}, at)); n != 1 {
		t.Fatal("closed", n, "stale sync attempts, want 1")
	}
	if rowCount(t, s, `select count(*) from workiq_attempts where id=? and status='failed' and error_code='workiq_interrupted'
and finished_at is not null and gap_count=1`, stale) != 1 ||
		rowCount(t, s, `select count(*) from workiq_attempts where id=? and status='open'`, probe) != 1 ||
		rowCount(t, s, `select count(*) from workiq_attempts where id in (?,?) and status='open'`, otherID, chatsID) != 2 ||
		rowCount(t, s, `select count(*) from workiq_attempts where id=? and error_code='workiq_failed'`, done) != 1 ||
		rowCount(t, s, `select count(*) from sync_runs where error_code='workiq_interrupted'`) != 1 {
		t.Fatal("stale closure touched the wrong attempts or lost the interrupted outcome")
	}
	if n := must(s.CloseStaleWorkIQAttempts(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "sync"}, at)); n != 0 {
		t.Fatal("a settled attempt was closed again")
	}
	must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	if _, err := s.CloseStaleWorkIQAttempts(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "sync"}, time.Time{}); err == nil {
		t.Fatal("a stale attempt was closed without a completion time")
	}
	must0(s.Close())
	if _, err := s.CloseStaleWorkIQAttempts(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "sync"}, at); err == nil {
		t.Fatal("a closed archive answered")
	}
}

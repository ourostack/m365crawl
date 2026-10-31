package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkIQMailCompleteNeedsARecordedWholeAccountSync(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("an archive without WorkIQ mail claimed a complete server sync")
	}
	attempt := seedCompletedWorkIQMail(t, s)
	if must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("an unfinished attempt counted as a complete server sync")
	}
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Hour), MailProof: &proof}))
	if !must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("a recorded whole-account sync was not found")
	}
	if must(s.WorkIQMailComplete(ctx, "outlook/Other")) {
		t.Fatal("one account's sync covered another account")
	}
	must0(s.SetWorkIQEnabled(ctx, false))
	if !must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("disabling WorkIQ hid retained server mail")
	}
	must(s.db.ExecContext(ctx, `update workiq_attempts set gap_count=1`))
	if must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("a sync with gaps counted as complete")
	}
}

func TestWorkIQMailCompleteOnOlderAndBrokenArchives(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.db.ExecContext(ctx, `delete from schema_migrations where version>=22`))
	if must(s.WorkIQMailComplete(ctx, mailAcct)) {
		t.Fatal("an older schema claimed WorkIQ mail")
	}
	must0(s.Close())
	if _, err := s.WorkIQMailComplete(ctx, mailAcct); err == nil {
		t.Fatal("a closed archive answered")
	}
}

package store

import (
	"context"
	"testing"
)

func TestMailThreadPropagatesConversationQueryFailureAfterSeed(t *testing.T) {
	s := newStore(t)
	msg := mailMsg(101, fInbox, "<synthetic@example.test>", "Synthetic", 1)
	msg.ConversationID = "SyntheticConversation"
	mustCommitMail(t, s, mailBatch(mailT0, msg))
	original := mailThreadConversationGroup
	t.Cleanup(func() { mailThreadConversationGroup = original })
	called := false
	mailThreadConversationGroup = func(actual *Store, ctx context.Context, seed MailRow) ([]int64, bool, error) {
		called = true
		if actual != s || seed.ConversationID != "SyntheticConversation" || seed.Account != mailAcct || seed.Rowid <= 0 {
			t.Fatal("successful_thread_seed_missing")
		}
		// MailGet has already completed; fail only the real subsequent conversation query, without timing/cancellation guessing.
		if _, err := actual.db.ExecContext(ctx, `drop table mail_messages`); err != nil {
			t.Fatal("synthetic_group_query_fault_failed")
		}
		return original(actual, ctx, seed)
	}
	rows, grouping, truncated, err := s.MailThread(context.Background(), mailAcct, 101)
	if !called || err == nil || rows != nil || grouping != "" || truncated {
		t.Fatal("thread_group_query_failure_not_propagated")
	}
}

package store

import (
	"context"
	"errors"
	"testing"
)

func TestMailThreadPropagatesCancellationAfterSuccessfulConversationQuery(t *testing.T) {
	s := newStore(t)
	a := mailMsg(101, fInbox, "<one@example.test>", "Synthetic", 2)
	b := mailMsg(102, fInbox, "<two@example.test>", "Synthetic", 1)
	a.ConversationID, b.ConversationID = "SyntheticConversation", "SyntheticConversation"
	mustCommitMail(t, s, mailBatch(mailT0, a, b))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := mailThreadConversationGroup
	t.Cleanup(func() { mailThreadConversationGroup = original })
	calls := 0
	mailThreadConversationGroup = func(actual *Store, queryCtx context.Context, seed MailRow) ([]int64, bool, error) {
		calls++
		if actual != s || queryCtx != ctx || seed.Account != mailAcct || seed.DetailKey != 101 ||
			seed.ConversationID != "SyntheticConversation" || seed.Rowid <= 0 {
			t.Fatal("successful_thread_seed_missing")
		}
		ids, linked, err := original(actual, queryCtx, seed)
		if err != nil || len(ids) != 2 || !linked {
			t.Fatal("real_conversation_query_preflight_failed")
		}
		cancel()
		select {
		case <-queryCtx.Done():
		default:
			t.Fatal("conversation_query_context_not_cancelled")
		}
		ids, linked, err = original(actual, queryCtx, seed)
		if !errors.Is(err, context.Canceled) || ids != nil || linked {
			t.Fatal("real_conversation_query_cancellation_not_propagated")
		}
		return ids, linked, err
	}
	rows, grouping, truncated, err := s.MailThread(ctx, mailAcct, 101)
	if calls != 1 || !errors.Is(err, context.Canceled) || rows != nil || grouping != "" || truncated {
		t.Fatal("cancelled_conversation_query_returned_partial_or_success")
	}
	mailThreadConversationGroup = original
	rows, grouping, truncated, err = s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || len(rows) != 2 || grouping != "conversation" || truncated ||
		rows[0].DetailKey != 101 || rows[1].DetailKey != 102 {
		t.Fatal("restored_conversation_query_could_not_retry")
	}
}

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

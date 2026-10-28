package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWorkIQMailRecheckReadsActualRemovedEvidenceAndScopedFrontiers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQMailAttempt(t, s)
	if must(s.WorkIQMailRecheckPending(ctx, attempt)) {
		t.Fatal("no removed evidence invented a pending census")
	}
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{[]byte(`{"id":"removed","@removed":{}}`)})
	if !must(s.WorkIQMailRecheckPending(ctx, attempt)) {
		t.Fatal("actual staged removed evidence did not survive as a census request")
	}
	for _, limit := range []int{0, 129} {
		if _, err := s.ConfirmWorkIQMailCensusIfFull(ctx, attempt, limit); err == nil {
			t.Fatal("invalid confirmation bound succeeded")
		}
	}
	must(s.CommitWorkIQMailGroup(ctx, attempt, ""))
	for done := false; !done; {
		done = must(s.ConfirmWorkIQMailCensusIfFull(ctx, attempt, 128))
	}
	first := must(s.WorkIQMailResetFrontiers(ctx, attempt, 0, 1))
	if len(first) != 1 {
		t.Fatal("bounded reset traversal did not return one actual frontier")
	}
	next := must(s.WorkIQMailResetFrontiers(ctx, attempt, first[0].ID, 128))
	if len(next) != 1 || first[0].ID >= next[0].ID || first[0].Resource == next[0].Resource {
		t.Fatal("bounded reset traversal skipped or repeated a resource")
	}
	if rows := must(s.WorkIQMailResetFrontiers(ctx, attempt, next[0].ID, 128)); len(rows) != 0 {
		t.Fatal("reset traversal did not terminate")
	}
	for _, args := range [][2]int64{{-1, 1}, {0, 0}, {0, 129}} {
		if _, err := s.WorkIQMailResetFrontiers(ctx, attempt, args[0], int(args[1])); err == nil {
			t.Fatal("invalid frontier bound succeeded")
		}
	}
	chat := seedWorkIQConversation(t, s)
	if _, err := s.WorkIQMailRecheckPending(ctx, chat); err == nil {
		t.Fatal("chat owner supplied a mail census request")
	}
	if _, err := s.WorkIQMailResetFrontiers(ctx, chat, 0, 1); err == nil {
		t.Fatal("chat owner supplied mail reset resources")
	}
	if _, err := s.ConfirmWorkIQMailCensusIfFull(ctx, chat, 1); err == nil {
		t.Fatal("chat owner supplied a mail confirmation")
	}
	must(s.db.ExecContext(ctx, `update workiq_frontiers set revision=-1`))
	if _, err := s.WorkIQMailResetFrontiers(ctx, attempt, 0, 128); err == nil {
		t.Fatal("corrupt revision became a baseline reset")
	}
	must(s.db.ExecContext(ctx, `update workiq_frontiers set revision=1`))
	must(s.db.ExecContext(ctx, `update workiq_frontiers set resource='https://foreign.invalid/me/mailFolders' where id=?`, first[0].ID))
	if _, err := s.WorkIQMailResetFrontiers(ctx, attempt, 0, 128); err == nil {
		t.Fatal("foreign resource became a baseline reset")
	}
}

func TestWorkIQMailRecheckReadFaultsRemainErrors(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, s *Store) {
		attempt := seedWorkIQMailAttempt(t, s)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{[]byte(`{"id":"removed","@removed":{}}`)})
		must(s.CommitWorkIQMailGroup(ctx, attempt, ""))
	}
	for name, op := range map[string]func(context.Context, *Store) error{
		"pending": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQMailRecheckPending(ctx, 1)
			return err
		},
		"frontiers": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQMailResetFrontiers(ctx, 1, 0, 128)
			return err
		},
		"confirmation": func(ctx context.Context, s *Store) error {
			_, err := s.ConfirmWorkIQMailCensusIfFull(ctx, 1, 128)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, setup, op) })
		t.Run("closed-"+name, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			must0(s.Close())
			if err := op(ctx, s); err == nil {
				t.Fatal("closed archive erased a pending census or returned reset resources")
			}
		})
	}
}

func TestWorkIQMailConditionalConfirmationNeverPromotesAnIncrementalInventory(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := seedWorkIQMailAttempt(t, s)
	if _, err := s.ConfirmWorkIQMailCensusIfFull(ctx, first, 128); err == nil {
		t.Fatal("missing census appeared complete")
	}
	resource := prepareRealMailInventory(t, s, first, []json.RawMessage{})
	must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
	must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	prepareRealMailInventory(t, s, next, []json.RawMessage{})
	if _, err := s.ConfirmWorkIQMailCensusIfFull(ctx, next, 128); err == nil {
		t.Fatal("incomplete retained membership became a census")
	}
	_, _, err := s.CarryWorkIQMailInventory(ctx, next, 0, 128)
	must0(err)
	if !must(s.ConfirmWorkIQMailCensusIfFull(ctx, next, 128)) ||
		rowCount(t, s, `select count(*) from workiq_censuses where attempt_id=? and confirmation_complete=1`, next) != 0 {
		t.Fatal("incremental inventory was silently promoted to full absence evidence")
	}
}

package store

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQPageProgressIsAtomicAndCompareAndSwap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	scope := WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}
	attempt := must(s.BeginWorkIQAttempt(ctx, scope, base))
	page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"error":null,"data":{"value":[],"@odata.nextLink":"/chats/c/messages?$skiptoken=A%2fb"}}]}}`)))
	batch := WorkIQPageBatch{AttemptID: attempt, Resource: "/chats/c/messages", Page: page, ObservedAt: base}
	must0(s.StageWorkIQPage(ctx, batch))
	if rowCount(t, s, `select count(*) from workiq_pages`) != 1 || rowCount(t, s, `select revision from workiq_frontiers`) != 1 {
		t.Fatal("data and cursor did not advance together")
	}

	if err := s.StageWorkIQPage(ctx, batch); err == nil {
		t.Fatal("stale writer advanced the same cursor twice")
	}
	if rowCount(t, s, `select count(*) from workiq_pages`) != 1 {
		t.Fatal("failed CAS inserted an extra page")
	}
	batch.ExpectedRevision = 1
	batch.Page.JSON = []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],"@odata.nextLink":"/chats/c/messages?tempauth=private-download"}}]}}`)
	if err := s.StageWorkIQPage(ctx, batch); err == nil {
		t.Fatal("changed cursor was persisted")
	}
	if rowCount(t, s, `select revision from workiq_frontiers`) != 1 || rowCount(t, s, `select count(*) from workiq_pages`) != 1 {
		t.Fatal("redacted cursor partially committed")
	}
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok_with_omissions", FinishedAt: base, Gaps: map[string]int{"workiq_message_identity_unverified": 1}}))
	if rowCount(t, s, `select count(*) from workiq_mail_receipts`) != 0 || rowCount(t, s, `select count(*) from sync_runs where source=''`) != 0 {
		t.Fatal("chat outcome fabricated whole-mailbox or ordinary sync freshness")
	}
}

func TestWorkIQAttemptAllocationRollsBackOnLastIDFailure(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	inj := injectFaults(t, s)
	inj.arm("lastid", 1)
	_, err := s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base)
	fired := inj.disarm()
	if !fired || err == nil {
		t.Fatal("attempt allocation did not expose the injected failure")
	}
	if rowCount(t, s, `select count(*) from workiq_attempts`) != 0 {
		t.Fatal("failed attempt allocation retained a successful insert")
	}
}

func TestWorkIQPageCannotLeaveItsFamilyOrResource(t *testing.T) {
	for _, tc := range []struct{ family, resource, next string }{
		{"mail", "/chats/c/messages", ""},
		{"chats", "/me/messages", ""},
		{"chats", "/chats/c/messages", "/chats/other/messages?$skiptoken=x"},
		{"mail", "/me/mailFolders/f/messages/delta", "/me/mailFolders/other/messages/delta?$skiptoken=x"},
	} {
		t.Run(tc.family+tc.resource+tc.next, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			must0(s.ApplyAccount(ctx, acctA))
			must0(s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main|mail", SourceAccounts: []string{"outlook/Main"}, Status: "failed"}))
			must0(s.SetWorkIQEnabled(ctx, true))
			account := acctA.TenantID + "/" + acctA.UserID
			if tc.family == "mail" {
				account = "outlook/Main"
			}
			attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: tc.family, Account: account}, base))
			body := `{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`
			if tc.next != "" {
				body = `{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],"@odata.nextLink":"` + tc.next + `"}}]}}`
			}
			page := must(workiq.DecodePage(ctx, []byte(body)))
			err := s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: tc.resource, Page: page, ObservedAt: base})
			if err == nil || rowCount(t, s, `select count(*) from workiq_pages`) != 0 || rowCount(t, s, `select count(*) from workiq_frontiers`) != 0 {
				t.Fatal("out-of-scope page changed durable source progress")
			}
		})
	}
}

func TestWorkIQStorageFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		t.Helper()
		ctx := context.Background()
		must0(s.ApplyAccount(ctx, acctA))
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<one@test.invalid>", "One", 0)))
		seedWorkIQAccountFacts(t, s)
		must0(s.SetWorkIQEnabled(ctx, true))
	}
	t.Run("settings read", func(t *testing.T) {
		sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQEnabled(ctx)
			return err
		})
	})
	t.Run("settings write", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error { return s.SetWorkIQEnabled(ctx, true) })
	})
	t.Run("accounts", func(t *testing.T) {
		sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQAccounts(ctx)
			return err
		})
	})
	t.Run("begin", func(t *testing.T) {
		sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, base)
			return err
		})
	})
	t.Run("stage", func(t *testing.T) {
		var id int64
		prepared := func(t *testing.T, s *Store) {
			setup(t, s)
			id = must(s.BeginWorkIQAttempt(context.Background(), WorkIQScope{Family: "mail", Account: mailAcct}, base))
		}
		page := must(workiq.DecodePage(context.Background(), []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
		sweepFaults(t, prepared, func(ctx context.Context, s *Store) error {
			return s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: id, Resource: "/me/mailFolders/f/messages/delta", Page: page, ObservedAt: base})
		})
	})
	t.Run("complete", func(t *testing.T) {
		var id int64
		prepared := func(t *testing.T, s *Store) {
			setup(t, s)
			id = must(s.BeginWorkIQAttempt(context.Background(), WorkIQScope{Family: "mail", Account: mailAcct}, base))
		}
		sweepFaults(t, prepared, func(ctx context.Context, s *Store) error {
			return s.CompleteWorkIQAttempt(ctx, id, WorkIQSummary{Status: "ok_with_omissions", FinishedAt: base, Gaps: map[string]int{"workiq_clock_unknown": 1}})
		})
	})
}

func TestWorkIQProgressRejectsInvalidStateWithoutMutation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	scope := WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}
	if _, err := s.BeginWorkIQAttempt(ctx, scope, base); err == nil {
		t.Fatal("disabled archive accepted a source attempt")
	}
	must0(s.SetWorkIQEnabled(ctx, true))
	if _, err := s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: "outlook/NeverRecorded"}, base); err == nil {
		t.Fatal("unknown archive account was created")
	}
	attempt := must(s.BeginWorkIQAttempt(ctx, scope, base))
	valid := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
	for _, batch := range []WorkIQPageBatch{
		{AttemptID: attempt, Resource: "/me/chats", Page: valid},
		{AttemptID: attempt, Resource: "//foreign.invalid", Page: valid, ObservedAt: base},
		{AttemptID: attempt, Resource: "/me/chats", Page: workiq.Page{}, ObservedAt: base},
		{AttemptID: attempt, Resource: "/me/chats", Page: valid, ObservedAt: base, Qualification: "other"},
	} {
		if err := s.StageWorkIQPage(ctx, batch); err == nil {
			t.Fatal("invalid source batch changed state")
		}
	}
	for _, summary := range []WorkIQSummary{
		{Status: "invented", FinishedAt: base},
		{Status: "ok"},
		{Status: "partial", FinishedAt: base, Gaps: map[string]int{"not_workiq": 1}},
	} {
		if err := s.CompleteWorkIQAttempt(ctx, attempt, summary); err == nil {
			t.Fatal("invalid source completion was accepted")
		}
	}
	if rowCount(t, s, `select count(*) from workiq_pages`) != 0 || rowCount(t, s, `select count(*) from workiq_gaps`) != 0 {
		t.Fatal("invalid progress retained partial evidence")
	}
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: base}))
	if err := s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: base}); err == nil {
		t.Fatal("a completed attempt was rewritten")
	}
}

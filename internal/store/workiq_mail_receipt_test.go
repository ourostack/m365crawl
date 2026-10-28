package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWorkIQMailCoveringFlagAloneCannotCreateADataReceipt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0, AllFolders: true, Folders: 99}))
	if rowCount(t, s, `select count(*) from workiq_mail_receipts`) != 0 ||
		rowCount(t, s, `select count(*) from workiq_attempts where id=? and all_folders=1`, attempt) != 0 {
		t.Fatal("a caller's summary flag masqueraded as an actual complete mailbox receipt")
	}
}

func seedCompletedWorkIQMail(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	resource := prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, resource, 1))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, "/me/mailFolders?includeHiddenFolders=true", 1))
	for done := false; !done; {
		done = must(s.ConfirmWorkIQMailCensusIfFull(ctx, attempt, 128))
	}
	return attempt
}

func TestWorkIQMailCompletionRecomputesAdmissionRatherThanTrustingFlags(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{
		`update workiq_censuses set complete=0`,
		`update workiq_censuses set inventory_ready=0`,
		`update workiq_censuses set carry_complete=0`,
		`update workiq_censuses set confirmation_complete=0`,
		`update workiq_censuses set enumeration_key='different'`,
		`update workiq_censuses set source_data_at='bad'`,
		`update workiq_censuses set source_data_at='2026-09-10T09:00:00Z'`,
		`update workiq_frontiers set admitted_revision=0`,
		`update workiq_pages set prepared_complete=0`,
		`update workiq_pages set expected_revision=-1`,
		`update workiq_pages set next_cursor='/me/mailFolders'`,
		`update workiq_frontiers set pending_cursor='different'`,
		`update workiq_census_members set admitted=0`,
		`update workiq_frontiers set cursor='',pending_cursor='';update workiq_pages set delta_cursor=''`,
		`update workiq_frontiers set resource='/me/mailFolders/different' where resource='/me/mailFolders?includeHiddenFolders=true'`,
		`insert into workiq_gaps(attempt_id,code,n) values(1,'workiq_test_gap',1)`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			attempt := seedCompletedWorkIQMail(t, s)
			must(s.db.ExecContext(ctx, mutation))
			before := dump(t, s)
			proof, err := s.InspectWorkIQMailCompletion(ctx, attempt)
			if err == nil || proof.file != nil || dump(t, s) != before {
				t.Fatal("corrupt admission became a receipt capability or mutated its read snapshot")
			}
		})
	}
	t.Run("pending actual removed page", func(t *testing.T) {
		s := newStore(t)
		attempt := seedWorkIQMailAttempt(t, s)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{[]byte(`{"id":"removed","@removed":{}}`)})
		if _, err := s.InspectWorkIQMailCompletion(ctx, attempt); err == nil {
			t.Fatal("pending census check acquired a completion capability")
		}
	})
	t.Run("missing census", func(t *testing.T) {
		s := newStore(t)
		attempt := seedWorkIQMailAttempt(t, s)
		if _, err := s.InspectWorkIQMailCompletion(ctx, attempt); err == nil {
			t.Fatal("missing inventory acquired a completion capability")
		}
	})
	t.Run("chat owner", func(t *testing.T) {
		s := newStore(t)
		attempt := seedWorkIQConversation(t, s)
		if _, err := s.InspectWorkIQMailCompletion(ctx, attempt); err == nil {
			t.Fatal("chat owner acquired a mail completion capability")
		}
	})
}

func TestWorkIQMailCompletionProofReadAndTransactionFaultsStayAtomic(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, s *Store) { seedCompletedWorkIQMail(t, s) }
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.InspectWorkIQMailCompletion(ctx, 1)
		return err
	})
	for _, kind := range []string{"begin", "commit"} {
		t.Run("inspection-"+kind, func(t *testing.T) {
			s := newStore(t)
			attempt := seedCompletedWorkIQMail(t, s)
			inj := injectFaults(t, s)
			inj.arm(kind, 1)
			proof, err := s.InspectWorkIQMailCompletion(ctx, attempt)
			if !inj.disarm() || !errors.Is(err, errInjected) || proof.file != nil {
				t.Fatal("failed snapshot returned a completion capability")
			}
		})
	}
	var proof WorkIQMailCompletionProof
	sweepFaults(t, func(t *testing.T, s *Store) {
		attempt := seedCompletedWorkIQMail(t, s)
		proof = must(s.InspectWorkIQMailCompletion(ctx, attempt))
	}, func(ctx context.Context, s *Store) error {
		return s.CompleteWorkIQAttempt(ctx, 1, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Second), MailProof: &proof})
	})
}

func TestWorkIQMailCompletionProofBindsEpochFileOwnerAndOutcome(t *testing.T) {
	ctx := context.Background()
	for _, mutation := range []string{"attempt", "account", "empty", "clock", "qualification", "revision", "file", "failed", "gap", "early"} {
		t.Run(mutation, func(t *testing.T) {
			s := newStore(t)
			attempt := seedCompletedWorkIQMail(t, s)
			proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
			summary := WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Second), MailProof: &proof}
			switch mutation {
			case "attempt":
				proof.attempt++
			case "account":
				proof.account = "outlook/other"
			case "empty":
				proof.file = nil
			case "clock":
				proof.dataAt = time.Time{}
			case "qualification":
				proof.qualification = "different"
			case "revision":
				stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders/inbox", map[string]any{"id": "inbox-id"})
			case "file":
				other := newStore(t)
				proof.file = must(os.Stat(other.cs.Path()))
			case "failed":
				summary.Status = "failed"
			case "gap":
				summary.Gaps = map[string]int{"workiq_test_gap": 1}
			case "early":
				summary.FinishedAt = mailT0.Add(-time.Hour)
			}
			before := dump(t, s)
			if err := s.CompleteWorkIQAttempt(ctx, attempt, summary); err == nil || dump(t, s) != before {
				t.Fatal("foreign/stale/gapped proof became a covering completion")
			}
		})
	}

	t.Run("safe proof representations", func(t *testing.T) {
		s := newStore(t)
		attempt := seedCompletedWorkIQMail(t, s)
		proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
		for _, text := range []string{fmt.Sprint(proof), fmt.Sprintf("%#v", proof)} {
			if strings.Contains(text, proof.account) || strings.Contains(text, proof.enumeration) || strings.Contains(text, s.cs.Path()) {
				t.Fatal("private receipt capability exposed owner/file/evidence")
			}
		}
	})
}

func TestWorkIQMailCompletionPhysicalFileFailuresCannotIssueReceipts(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"before", "after", "replaced", "consume"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			attempt := seedCompletedWorkIQMail(t, s)
			proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
			other := newStore(t)
			old := workIQInventoryStat
			t.Cleanup(func() { workIQInventoryStat = old })
			n := 0
			workIQInventoryStat = func(path string) (os.FileInfo, error) {
				n++
				if mode == "before" || mode == "consume" || mode == "after" && n == 2 {
					return nil, errInjected
				}
				if mode == "replaced" && n == 2 {
					return os.Stat(other.cs.Path())
				}
				return old(path)
			}
			before := dump(t, s)
			if mode == "consume" {
				err := s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Second), MailProof: &proof})
				if !errors.Is(err, errInjected) {
					t.Fatal("receipt lost its physical-file stat failure")
				}
			} else {
				next, err := s.InspectWorkIQMailCompletion(ctx, attempt)
				if err == nil || next.file != nil {
					t.Fatal("failed/replaced physical archive issued a receipt capability")
				}
			}
			if dump(t, s) != before {
				t.Fatal("physical-file failure partially committed a receipt")
			}
		})
	}
}

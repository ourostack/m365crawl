package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQProbeCanonicalFolderCountsIncludeEveryProvenance(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{
		graphMailFixture("one", "inbox-id", "one@example.test"),
		graphMailFixture("two", "inbox-id", "two@example.test"),
	})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "two@example.test"))
	n := must(s.WorkIQMailFolderCount(ctx, mailAcct, "inbox-id"))
	// Initial cache-first folder precedence is not a proven server move or a native-folder bridge.
	if n == nil || *n != 1 {
		t.Fatal("current canonical folder count guessed an unproven native-folder bridge")
	}
	for _, scope := range [][2]string{{"", "inbox-id"}, {mailAcct, ""}} {
		if _, err := s.WorkIQMailFolderCount(ctx, scope[0], scope[1]); err == nil {
			t.Fatal("invalid folder count scope became a known count")
		}
	}
	must0(s.Close())
	if _, err := s.WorkIQMailFolderCount(ctx, mailAcct, "inbox-id"); err == nil {
		t.Fatal("closed archive returned a folder count")
	}
}

func TestWorkIQProbeChatCountsExcludeWrongClassesOwnersAndClocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	fact := workiq.AccountFact{ID: acctA.TenantID + "/" + acctA.UserID, Family: "chats", TenantID: acctA.TenantID, UserID: acctA.UserID}
	for i, class := range []string{"Text", "RichText/Html", "RichText/Media_Card", "RichText/Media_CallRecording", "other"} {
		m := msg(acctA, "chat", string(rune('a'+i)), "synthetic", base)
		m.MessageType = class
		must(s.ApplyMessages(ctx, []teamsdesktop.Message{m}))
	}
	if n := must(s.WorkIQChatCoverageCount(ctx, fact, "chat", base, base.Add(time.Hour))); n != 4 {
		t.Fatal("counts changed the documented comparable class set")
	}
	for _, bad := range []workiq.AccountFact{{}, {ID: "different", Family: "chats", TenantID: fact.TenantID, UserID: fact.UserID}} {
		if _, err := s.WorkIQChatCoverageCount(ctx, bad, "chat", base, base); err == nil {
			t.Fatal("invalid chat owner received a count")
		}
	}
	must(s.db.ExecContext(ctx, `update messages set sent_at='corrupt' where id='a'`))
	if _, err := s.WorkIQChatCoverageCount(ctx, fact, "chat", base, base.Add(time.Hour)); err == nil {
		t.Fatal("corrupt source time became a positive count")
	}
	must0(s.Close())
	if _, err := s.WorkIQChatCoverageCount(ctx, fact, "chat", base, base); err == nil {
		t.Fatal("closed archive returned chat counts")
	}
}

func TestWorkIQProbeCountReadFaultsDoNotInventZeroCounts(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		attempt := setupWorkIQMailControl(t, s, true)
		_ = must(s.CommitWorkIQMailGroup(context.Background(), attempt, "one@example.test"))
	}, func(ctx context.Context, s *Store) error {
		_, err := s.WorkIQMailFolderCount(ctx, mailAcct, "inbox-id")
		return err
	})
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		must0(s.ApplyAccount(context.Background(), acctA))
		must(s.ApplyMessages(context.Background(), []teamsdesktop.Message{msg(acctA, "chat", "one", "synthetic", base)}))
	}, func(ctx context.Context, s *Store) error {
		_, err := s.WorkIQChatCoverageCount(ctx, workiq.AccountFact{ID: acctA.TenantID + "/" + acctA.UserID,
			Family: "chats", TenantID: acctA.TenantID, UserID: acctA.UserID}, "chat", base, base.Add(time.Hour))
		return err
	})
}

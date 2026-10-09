package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestMailConversationAndRecipientRoles(t *testing.T) {
	s := newStore(t)
	m := mailMsg(101, fInbox, "<one@example.test>", "first subject", 2)
	m.ConversationID = "conversation-one"
	m.Recipients = []outlookmail.Recipient{
		{Name: "To", Address: "to@example.test", Role: "to"},
		{Name: "Cc", Address: "cc@example.test", Role: "cc"},
		{Name: "Bcc", Address: "bcc@example.test", Role: "bcc"},
	}
	n := mailMsg(102, fInbox, "<two@example.test>", "different subject", 1)
	n.ConversationID = m.ConversationID
	mustCommitMail(t, s, mailBatch(mailT0, m, n))
	got, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != m.ConversationID {
		t.Fatalf("conversation = %q, want %q", got.ConversationID, m.ConversationID)
	}
	var roles []string
	for _, r := range got.Recipients {
		roles = append(roles, r.Role)
		if r.KindRaw != 0 {
			t.Fatalf("Windows recipient raw kind = %d, want 0", r.KindRaw)
		}
	}
	if !slices.Equal(roles, []string{"to", "cc", "bcc"}) {
		t.Fatalf("recipient roles = %v", roles)
	}
	rows, grouping, truncated, err := s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || grouping != "conversation" || truncated || len(rows) != 2 {
		t.Fatalf("thread = %d/%q/%t/%v, want 2/conversation/false/nil", len(rows), grouping, truncated, err)
	}
	other := mailMsg(103, fInbox, "<other@example.test>", "third subject", 0)
	other.Account, other.ConversationID = "outlook/other", m.ConversationID
	b := mailBatch(mailT0, other)
	b.Account = other.Account
	mustCommitMail(t, s, b)
	rows, grouping, _, err = s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || grouping != "conversation" || len(rows) != 2 {
		t.Fatalf("conversation leaked another account: %d/%q/%v", len(rows), grouping, err)
	}
}

func TestMailConversationSingleFallbackAndLateFill(t *testing.T) {
	s := newStore(t)
	a := mailMsg(101, fInbox, "<first@example.test>", "shared subject", 1)
	b := mailMsg(102, fInbox, "<second@example.test>", "shared subject", 0)
	a.ConversationID = "only-one"
	mustCommitMail(t, s, mailBatch(mailT0, a, b))
	rows, grouping, _, err := s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || grouping != "subject" || len(rows) != 2 {
		t.Fatalf("singleton fallback = %d/%q/%v", len(rows), grouping, err)
	}
	b.ConversationID = a.ConversationID
	res := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), a, b))
	if res.Updated != 1 {
		t.Fatalf("late conversation fill = %+v, want one update", res)
	}
	_, grouping, _, err = s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || grouping != "conversation" {
		t.Fatalf("filled conversation grouping = %q/%v", grouping, err)
	}
	if res := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), a, b)); res.Updated != 0 {
		t.Fatalf("repeated metadata changed a message: %+v", res)
	}
}

func TestMailConversationCapSeedAndGone(t *testing.T) {
	s := newStore(t)
	msgs := make([]outlookmail.Message, mailThreadCap+2)
	for i := range msgs {
		msgs[i] = mailMsg(uint32(i+1), fInbox, fmt.Sprintf("<m%d@example.test>", i), fmt.Sprintf("subject %d", i), 0)
		msgs[i].ConversationID = "large-conversation"
		msgs[i].Received = mailT0.Add(time.Duration(i) * time.Minute)
	}
	mustCommitMail(t, s, mailBatch(mailT0, msgs...))
	if _, err := s.db.Exec(`update mail_messages set gone_at='2026-09-11T00:00:00.000Z' where detail_key=1`); err != nil {
		t.Fatal(err)
	}
	rows, grouping, truncated, err := s.MailThread(context.Background(), mailAcct, uint32(len(msgs)))
	if err != nil || grouping != "conversation" || !truncated || len(rows) != mailThreadCap {
		t.Fatalf("capped conversation = %d/%q/%t/%v", len(rows), grouping, truncated, err)
	}
	if !slices.ContainsFunc(rows, func(m MailRow) bool { return m.DetailKey == uint32(len(msgs)) }) {
		t.Fatal("conversation cap dropped the seed")
	}
	rows, _, _, err = s.MailThread(context.Background(), mailAcct, 1)
	if err != nil || !slices.ContainsFunc(rows, func(m MailRow) bool { return m.DetailKey == 1 && !m.GoneAt.IsZero() }) {
		t.Fatal("conversation dropped its gone seed")
	}
}

func TestMailNativeHeaderAttachmentFlag(t *testing.T) {
	s := newStore(t)
	m := mailMsg(101, fInbox, "<one@example.test>", "attachment state", 1)
	no := false
	m.HasAttachments = &no
	m.Attachments = []outlookmail.Attachment{{Key: 1, Name: "metadata-only", Inline: false}}
	mustCommitMail(t, s, mailBatch(mailT0, m))
	got, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || got.HasAttachments || len(got.Attachments) != 1 {
		t.Fatalf("native header false overridden by metadata: %+v/%v", got, err)
	}

	yes := true
	m.HasAttachments, m.Attachments = &yes, nil
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), m))
	got, err = s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || !got.HasAttachments {
		t.Fatalf("native header true without metadata lost: %+v/%v", got, err)
	}
	m.HasAttachments = &no
	mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), m))
	got, err = s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || got.HasAttachments {
		t.Fatalf("native header false did not update: %+v/%v", got, err)
	}
}

func TestMailConversationGroupReportsQueryFailure(t *testing.T) {
	s := newStore(t)
	if _, err := s.db.Exec(`drop table mail_messages`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.conversationGroup(context.Background(), MailRow{Account: mailAcct, ConversationID: "synthetic", Rowid: 1}); err == nil {
		t.Fatal("conversation query failure was hidden")
	}
}

func TestMailEmptyConversationRetainsReplyChainAndMacRoles(t *testing.T) {
	s := newStore(t)
	a := mailMsg(101, fInbox, "<first@example.test>", "first subject", 2)
	b := mailMsg(102, fInbox, "<second@example.test>", "other subject", 1)
	b.InReplyTo = a.MessageID
	mustCommitMail(t, s, mailBatch(mailT0, a, b))
	rows, grouping, truncated, err := s.MailThread(context.Background(), mailAcct, 101)
	if err != nil || grouping != "reply_chain" || truncated || len(rows) != 2 {
		t.Fatalf("Mac fallback = %d/%q/%t/%v", len(rows), grouping, truncated, err)
	}
	if n := rowCount(t, s, `select count(*) from mail_recipients where role is null`); n != 2 {
		t.Fatalf("Mac null recipient roles = %d, want 2", n)
	}
}

func TestMailReplacementRewritesRolesAndConversation(t *testing.T) {
	s := newStore(t)
	m := mailMsg(101, fInbox, "<old@example.test>", "old message", 2)
	m.ConversationID = "old-conversation"
	m.Recipients = []outlookmail.Recipient{{Name: "To", Role: "to"}}
	mustCommitMail(t, s, mailBatch(mailT0, m))
	m.MessageID, m.ConversationID = "<new@example.test>", "new-conversation"
	m.Recipients = []outlookmail.Recipient{{Name: "Bcc", Role: "bcc"}}
	if res := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), m)); res.Replaced != 1 {
		t.Fatalf("replacement = %+v", res)
	}
	got, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || got.ConversationID != "new-conversation" || len(got.Recipients) != 1 || got.Recipients[0].Role != "bcc" {
		t.Fatalf("replacement metadata = %+v/%v", got, err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQChatSelectionReadsOnlyArchivedActivityAndKeepsEmptyWindows(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	fact := workiq.AccountFact{ID: acctA.TenantID + "/" + acctA.UserID, Family: "chats", TenantID: acctA.TenantID, UserID: acctA.UserID}
	if at := must(s.WorkIQChatActivity(ctx, fact, "server-only")); !at.IsZero() {
		t.Fatal("server-only chat was assigned an invented activity clock")
	}
	c := conv(acctA, "empty-window", "group", "retained")
	c.LastMessageAt = base.Add(-800 * 24 * time.Hour)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{c}))
	if at := must(s.WorkIQChatActivity(ctx, fact, c.ID)); !at.Equal(c.LastMessageAt) {
		t.Fatal("old activity was filtered by the default source window")
	}
	m := msg(acctA, "message-only", "m", "retained", base)
	m.SentAt = base
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m}))
	if at := must(s.WorkIQChatActivity(ctx, fact, "message-only")); !at.Equal(base) {
		t.Fatal("message-only archived chat lost local selection priority")
	}
	for _, bad := range []workiq.AccountFact{{}, {ID: "different", Family: "chats", TenantID: fact.TenantID, UserID: fact.UserID},
		{ID: "unknown/user", Family: "chats", TenantID: "unknown", UserID: "user"}} {
		if _, err := s.WorkIQChatActivity(ctx, bad, c.ID); err == nil {
			t.Fatal("invalid or unarchived owner received activity")
		}
	}
	if _, err := s.WorkIQChatActivity(ctx, fact, ""); err == nil {
		t.Fatal("empty chat key was accepted")
	}
	must(s.db.ExecContext(ctx, `update conversations set last_message_at='broken' where id='empty-window'`))
	if _, err := s.WorkIQChatActivity(ctx, fact, c.ID); err == nil {
		t.Fatal("corrupt activity was silently ranked")
	}
	must(s.db.ExecContext(ctx, `alter table conversations rename column last_message_at to unavailable_clock`))
	if _, err := s.WorkIQChatActivity(ctx, fact, c.ID); err == nil {
		t.Fatal("activity query failure appeared to be unknown activity")
	}
	must0(s.Close())
	if _, err := s.WorkIQChatActivity(ctx, fact, c.ID); err == nil {
		t.Fatal("closed archive returned activity")
	}
}

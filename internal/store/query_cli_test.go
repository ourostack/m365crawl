package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestLastSuccess(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	got, err := s.LastSuccess(ctx)
	if err != nil || !got.IsZero() {
		t.Fatalf("empty archive: %v %v", got, err)
	}
	must0(s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base.Add(time.Second), Status: "ok", Accounts: []string{"*"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: base.Add(time.Hour), FinishedAt: base.Add(time.Hour + time.Second), Status: "failed", Accounts: []string{"*"}}))
	got, err = s.LastSuccess(ctx)
	if err != nil || !got.Equal(base.Add(time.Second)) {
		t.Fatalf("a failed run must not count: %v %v", got, err)
	}
	must0(s.RecordRun(ctx, Run{StartedAt: base.Add(2 * time.Hour), FinishedAt: base.Add(2*time.Hour + time.Second), Status: "unchanged", Accounts: []string{"*"}}))
	got, _ = s.LastSuccess(ctx)
	if !got.Equal(base.Add(2*time.Hour + time.Second)) {
		t.Fatalf("unchanged counts as success: %v", got)
	}
}

func TestMessageHTML(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c", "m1", "hi", base), msg(acctB, "c", "m1", "other", base)}))
	keys := []MessageKey{{TenantID: acctA.TenantID, UserID: acctA.UserID, ConversationID: "c", ID: "m1"}, {TenantID: "x", UserID: "y", ConversationID: "c", ID: "zz"}}
	got, err := s.MessageHTML(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[keys[0]] != "<p>hi</p>" {
		t.Fatalf("html = %v", got)
	}
}

func TestMessageWindow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if oldest, newest, err := s.MessageWindow(ctx, nil); err != nil || !oldest.IsZero() || !newest.IsZero() {
		t.Fatalf("empty archive: %v %v %v", oldest, newest, err)
	}
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c", "m1", "a", base), msg(acctA, "c", "m2", "b", base.Add(48*time.Hour)), msg(acctB, "c", "m1", "c", base.Add(-time.Hour))}))
	if oldest, newest, err := s.MessageWindow(ctx, nil); err != nil || !oldest.Equal(base.Add(-time.Hour)) || !newest.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("every account: %v %v %v", oldest, newest, err)
	}
	if oldest, newest, err := s.MessageWindow(ctx, &acctA); err != nil || !oldest.Equal(base) || !newest.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("one account: %v %v %v", oldest, newest, err)
	}
	_ = s.Close()
	if _, _, err := s.MessageWindow(ctx, nil); err == nil {
		t.Fatal("a closed archive must fail")
	}
}

func TestHasConversation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "19:held@thread.v2", "Chat", "Held")}))
	for _, c := range []struct {
		acct *teamsdesktop.Account
		id   string
		want bool
	}{
		{nil, "19:held@thread.v2", true},
		{&acctA, "19:held@thread.v2", true},
		{&acctB, "19:held@thread.v2", false},
		{nil, "19:other@thread.v2", false},
	} {
		if ok, err := s.HasConversation(ctx, c.acct, c.id); err != nil || ok != c.want {
			t.Errorf("%v %s: %v %v", c.acct, c.id, ok, err)
		}
	}
	_ = s.Close()
	if _, err := s.HasConversation(ctx, nil, "x"); err == nil {
		t.Fatal("a closed archive must fail")
	}
}

func TestHasAccount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	if ok, err := s.HasAccount(ctx, acctA); err != nil || !ok {
		t.Fatalf("held account: %v %v", ok, err)
	}
	if ok, err := s.HasAccount(ctx, acctB); err != nil || ok {
		t.Fatalf("other account: %v %v", ok, err)
	}
	_ = s.Close()
	if _, err := s.HasAccount(ctx, acctA); err == nil {
		t.Fatal("a closed archive must fail")
	}
}

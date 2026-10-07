package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestUnreadDefaultsToNonChannelKinds(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	chat := conv(acctA, "chat1", "Chat", "A chat")
	chat.ReadHorizonAt = base
	meet := conv(acctA, "meet1", "Meeting", "A meeting")
	meet.ReadHorizonAt = base
	topic := conv(acctA, "19:t@thread.tacv2", "Topic", "Stale channel")
	topic.ReadHorizonAt = base.Add(-400 * 24 * time.Hour)
	space := conv(acctA, "19:s@thread.skype", "Space", "Stale team")
	space.ReadHorizonAt = base.Add(-400 * 24 * time.Hour)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{chat, meet, topic, space}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "chat1", "c1", "chat", base.Add(time.Minute)),
		msg(acctA, "meet1", "e1", "meet", base.Add(time.Minute)),
		msg(acctA, "19:t@thread.tacv2", "t1", "topic", base.Add(time.Minute)),
		msg(acctA, "19:s@thread.skype", "s1", "space", base.Add(time.Minute)),
	}))
	rows, _ := must2(s.Unread(ctx, Filter{Account: &acctA}))
	if !eqStrings(ids(rows), []string{"e1", "c1"}) && !eqStrings(ids(rows), []string{"c1", "e1"}) {
		t.Fatalf("default unread: %v", ids(rows))
	}
	rows, _ = must2(s.Messages(ctx, Filter{Account: &acctA, Unread: true}))
	if len(rows) != 2 {
		t.Fatalf("messages unread: %v", ids(rows))
	}
	rows, _ = must2(s.Search(ctx, "topic", Filter{Account: &acctA, Unread: true}))
	if len(rows) != 0 {
		t.Fatalf("search unread: %v", ids(rows))
	}
	rows, _ = must2(s.Unread(ctx, Filter{Account: &acctA, IncludeChannels: true}))
	if len(rows) != 4 {
		t.Fatalf("include channels: %v", ids(rows))
	}
	rows, _ = must2(s.Search(ctx, "topic", Filter{Account: &acctA, Unread: true, IncludeChannels: true}))
	if !eqStrings(ids(rows), []string{"t1"}) {
		t.Fatalf("search include channels: %v", ids(rows))
	}
}

func TestUnreadChannelnessUsesKindOrID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	lower := conv(acctA, "19:l@thread.v2", "topic", "Lowercase kind")
	lower.ReadHorizonAt = base
	shaped := conv(acctA, "19:x@thread.skype", "Chat", "Chat kind, channel id")
	shaped.ReadHorizonAt = base
	plain := conv(acctA, "19:p@thread.v2", "Chat", "Plain chat")
	plain.ReadHorizonAt = base
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{lower, shaped, plain}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "19:l@thread.v2", "l1", "l", base.Add(time.Minute)),
		msg(acctA, "19:x@thread.skype", "x1", "x", base.Add(time.Minute)),
		msg(acctA, "19:p@thread.v2", "p1", "p", base.Add(time.Minute)),
	}))
	rows, _ := must2(s.Unread(ctx, Filter{Account: &acctA}))
	if !eqStrings(ids(rows), []string{"p1"}) {
		t.Fatalf("default: %v", ids(rows))
	}
	rows, _ = must2(s.Unread(ctx, Filter{Account: &acctA, IncludeChannels: true}))
	if len(rows) != 3 {
		t.Fatalf("include channels: %v", ids(rows))
	}
}

func TestMentionsMeFromActivity(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	person := msg(acctA, "c", "p1", "deploy person", base)
	person.MentionsMe = true
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		person,
		msg(acctA, "c", "ch1", "deploy channel", base.Add(time.Minute)),
		msg(acctA, "c", "tag1", "deploy tag", base.Add(2*time.Minute)),
		msg(acctA, "c", "like1", "deploy reacted", base.Add(3*time.Minute)),
		msg(acctA, "c", "none1", "deploy none", base.Add(4*time.Minute)),
		msg(acctB, "c", "ch1", "deploy other account", base),
	}))
	a := func(id, typ, msgID string) teamsdesktop.Activity {
		return teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: id, Type: typ, At: base, ConversationID: "c", MessageID: msgID}
	}
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{
		a("a1", "mention", "ch1"), a("a1b", "mentionInChat", "ch1"), // two mention items on one message
		a("a2", "mentionInChat", "tag1"), a("a3", "reaction", "like1"), a("a4", "reply", "none1"),
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a5", Type: "mention", At: base, ConversationID: "other", MessageID: "none1"},
	}))
	// Messages list oldest first, Search newest first; account B's ch1 has no activity.
	for name, c := range map[string]struct {
		run  func() []MessageRow
		want []string
	}{
		"messages": {func() []MessageRow { r, _ := must2(s.Messages(ctx, Filter{MentionsMe: true})); return r }, []string{"p1", "ch1", "tag1"}},
		"search":   {func() []MessageRow { r, _ := must2(s.Search(ctx, "deploy", Filter{MentionsMe: true})); return r }, []string{"tag1", "ch1", "p1"}},
	} {
		got, want := c.run(), c.want
		if !eqStrings(ids(got), want) {
			t.Fatalf("%s: %v, want %v", name, ids(got), want)
		}
		for _, r := range got {
			if !r.MentionsMe || r.UserID != acctA.UserID {
				t.Fatalf("%s: %+v", name, r)
			}
		}
	}
	all, _ := must2(s.Messages(ctx, Filter{Account: &acctA}))
	if len(all) != 5 {
		t.Fatalf("all: %v", ids(all))
	}
	for _, r := range all {
		if w := r.ID == "p1" || r.ID == "ch1" || r.ID == "tag1"; r.MentionsMe != w {
			t.Fatalf("%s mentions_me=%v", r.ID, r.MentionsMe)
		}
	}
	// Activity arriving in a later sync updates the result with no message rewrite.
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{a("a6", "mention", "none1")}))
	got, _ := must2(s.Messages(ctx, Filter{Account: &acctA, MentionsMe: true}))
	if !eqStrings(ids(got), []string{"p1", "ch1", "tag1", "none1"}) {
		t.Fatalf("late activity: %v", ids(got))
	}
}

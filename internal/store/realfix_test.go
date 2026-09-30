package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
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
		a("a1", "mention", "ch1"), a("a2", "mentionInChat", "tag1"), a("a3", "reaction", "like1"), a("a4", "reply", "none1"),
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a5", Type: "mention", At: base, ConversationID: "other", MessageID: "none1"},
	}))
	for name, run := range map[string]func() []MessageRow{
		"messages": func() []MessageRow { r, _ := must2(s.Messages(ctx, Filter{MentionsMe: true})); return r },
		"search":   func() []MessageRow { r, _ := must2(s.Search(ctx, "deploy", Filter{MentionsMe: true})); return r },
	} {
		got := run()
		if len(got) != 4 { // p1, ch1 and tag1 for account A plus ch1's... see below
			// account B's ch1 has no activity, so exactly three rows are expected
			if len(got) != 3 {
				t.Fatalf("%s: %v", name, ids(got))
			}
		}
		set := map[string]bool{}
		for _, r := range got {
			set[r.ID+"/"+r.UserID] = true
			if !r.MentionsMe {
				t.Fatalf("%s: mentions_me false for %s", name, r.ID)
			}
		}
		for _, want := range []string{"p1/" + acctA.UserID, "ch1/" + acctA.UserID, "tag1/" + acctA.UserID} {
			if !set[want] {
				t.Fatalf("%s: missing %s in %v", name, want, set)
			}
		}
		if len(set) != 3 {
			t.Fatalf("%s: %v", name, set)
		}
	}
	all, _ := must2(s.Messages(ctx, Filter{Account: &acctA}))
	for _, r := range all {
		if want := r.ID == "p1" || r.ID == "ch1" || r.ID == "tag1"; r.MentionsMe != want {
			t.Fatalf("%s mentions_me=%v", r.ID, r.MentionsMe)
		}
	}
	// Activity arriving in a later sync updates the result with no message rewrite.
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{a("a6", "mention", "none1")}))
	got, _ := must2(s.Messages(ctx, Filter{Account: &acctA, MentionsMe: true}))
	if len(got) != 4 {
		t.Fatalf("late activity: %v", ids(got))
	}
}

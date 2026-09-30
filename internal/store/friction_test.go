package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

func seedSystem(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s := newStore(t)
	cs := []teamsdesktop.Conversation{
		conv(acctA, "19:real@thread.v2", "Chat", "Real chat"),
		conv(acctA, "48:notifications", "Chat", "Feed"),
		conv(acctA, "48:calllogs", "Chat", "Calls"),
		conv(acctA, "48:annotations", "Chat", "Notes on messages"),
		conv(acctA, "48:notes", "Chat", "Self notes"),
	}
	for i := range cs {
		cs[i].ReadHorizonAt = base
		cs[i].LastMessageAt = base.Add(time.Duration(i+1) * time.Minute)
	}
	must(s.ApplyConversations(ctx, cs))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "19:real@thread.v2", "r1", "shared word", base.Add(time.Minute)),
		msg(acctA, "48:notifications", "n1", "shared word", base.Add(time.Minute)),
		msg(acctA, "48:calllogs", "l1", "shared word", base.Add(time.Minute)),
		msg(acctA, "48:annotations", "a1", "shared word", base.Add(time.Minute)),
		msg(acctA, "48:notes", "s1", "shared word", base.Add(time.Minute)),
	}))
	return s
}

func TestSystemConversationIDsHelper(t *testing.T) {
	got := SystemConversationIDs()
	if !eqStrings(got, []string{"48:notifications", "48:calllogs", "48:annotations"}) {
		t.Fatalf("system ids: %v", got)
	}
	got[0] = "x" // a copy: callers cannot change the list
	if SystemConversationIDs()[0] != "48:notifications" {
		t.Fatal("list is shared")
	}
}

func TestSystemConversationsExcludedByDefault(t *testing.T) {
	ctx := context.Background()
	s := seedSystem(t)
	f := Filter{Account: &acctA}
	rows, _ := must2(s.Search(ctx, "shared", f))
	if len(rows) != 2 {
		t.Fatalf("search: %v", ids(rows))
	}
	rows, _ = must2(s.Messages(ctx, f))
	if !eqStrings(ids(rows), []string{"r1", "s1"}) && !eqStrings(ids(rows), []string{"s1", "r1"}) {
		t.Fatalf("messages: %v", ids(rows))
	}
	rows, _ = must2(s.Unread(ctx, f))
	if len(rows) != 2 {
		t.Fatalf("unread: %v", ids(rows))
	}
	rows = must(s.Thread(ctx, "48:notifications", "n1", f))
	if len(rows) != 0 {
		t.Fatalf("thread: %v", ids(rows))
	}
	convs, _ := must2(s.Conversations(ctx, "", "", f))
	if len(convs) != 2 {
		t.Fatalf("conversations: %d", len(convs))
	}
	f.IncludeSystem = true
	rows, _ = must2(s.Search(ctx, "shared", f))
	if len(rows) != 5 {
		t.Fatalf("search include system: %v", ids(rows))
	}
	rows, _ = must2(s.Messages(ctx, f))
	if len(rows) != 5 {
		t.Fatalf("messages include system: %v", ids(rows))
	}
	rows, _ = must2(s.Unread(ctx, f))
	if len(rows) != 5 {
		t.Fatalf("unread include system: %v", ids(rows))
	}
	rows = must(s.Thread(ctx, "48:notifications", "n1", f))
	if len(rows) != 1 {
		t.Fatalf("thread include system: %v", ids(rows))
	}
	convs, _ = must2(s.Conversations(ctx, "", "", f))
	if len(convs) != 5 {
		t.Fatalf("conversations include system: %d", len(convs))
	}
}

func TestActivityJoinSkipsSystemMessages(t *testing.T) {
	ctx := context.Background()
	s := seedSystem(t)
	_, _, err := s.ApplyActivityChanges(ctx, []teamsdesktop.Activity{
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "x1", Type: "mention", At: base.Add(time.Minute), ConversationID: "48:notifications", MessageID: "n1", Raw: []byte(`{}`)},
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "x2", Type: "mention", At: base.Add(2 * time.Minute), ConversationID: "19:real@thread.v2", MessageID: "r1", Raw: []byte(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := must2(s.Activity(ctx, ActivityFilter{Account: &acctA}))
	text := map[string]string{}
	for _, r := range rows {
		text[r.ID] = r.MessageText
	}
	if text["x1"] != "" || text["x2"] != "shared word" {
		t.Fatalf("activity joins: %v", text)
	}
	rows, _ = must2(s.Activity(ctx, ActivityFilter{Account: &acctA, IncludeSystem: true}))
	for _, r := range rows {
		if r.ID == "x1" && r.MessageText != "shared word" {
			t.Fatalf("include system should join: %+v", r)
		}
	}
}

func TestUnreadByConversation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := conv(acctA, "19:a@thread.v2", "Chat", "Chat A")
	a.ReadHorizonAt = base
	b := conv(acctA, "19:b@thread.v2", "Chat", "Chat B")
	b.ReadHorizonAt = base
	ch := conv(acctA, "19:c@thread.tacv2", "Topic", "Channel C")
	ch.ReadHorizonAt = base
	sys := conv(acctA, "48:notifications", "Chat", "Feed")
	sys.ReadHorizonAt = base
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{a, b, ch, sys}))
	b3 := msg(acctA, "19:b@thread.v2", "b3", "x", base.Add(5*time.Minute))
	b3.Link = "https://teams.example/b3"
	own := msg(acctA, "19:a@thread.v2", "own", "mine", base.Add(9*time.Minute))
	own.SenderID = selfMRI(acctA)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "19:a@thread.v2", "a1", "x", base.Add(1*time.Minute)),
		msg(acctA, "19:b@thread.v2", "b1", "x", base.Add(2*time.Minute)),
		msg(acctA, "19:b@thread.v2", "b2", "x", base.Add(3*time.Minute)),
		b3,
		own,
		msg(acctA, "19:c@thread.tacv2", "c1", "x", base.Add(4*time.Minute)),
		msg(acctA, "19:c@thread.tacv2", "c2", "x", base.Add(6*time.Minute)),
		msg(acctA, "19:c@thread.tacv2", "c3", "x", base.Add(7*time.Minute)),
		msg(acctA, "19:c@thread.tacv2", "c4", "x", base.Add(8*time.Minute)),
		msg(acctA, "48:notifications", "n1", "x", base.Add(time.Minute)),
	}))
	rows, trunc := must2(s.UnreadByConversation(ctx, Filter{Account: &acctA}))
	if trunc || len(rows) != 2 || rows[0].ConversationID != "19:b@thread.v2" || rows[1].ConversationID != "19:a@thread.v2" {
		t.Fatalf("default: %+v", rows)
	}
	r := rows[0]
	if r.UnreadCount != 3 || r.Kind != "Chat" || r.DisplayName != "Chat B" ||
		!r.OldestUnreadAt.Equal(base.Add(2*time.Minute)) || !r.NewestUnreadAt.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("row: %+v", r)
	}
	if r.Link != "https://teams.example/b3" {
		t.Fatalf("link: %+v", r)
	}
	rows, _ = must2(s.UnreadByConversation(ctx, Filter{Account: &acctA, IncludeChannels: true}))
	if len(rows) != 3 || rows[0].ConversationID != "19:c@thread.tacv2" || rows[0].UnreadCount != 4 {
		t.Fatalf("include channels: %+v", rows)
	}
	rows, trunc = must2(s.UnreadByConversation(ctx, Filter{Account: &acctA, IncludeChannels: true, Limit: 2}))
	if !trunc || len(rows) != 2 {
		t.Fatalf("limit: %+v %v", rows, trunc)
	}
	rows, _ = must2(s.UnreadByConversation(ctx, Filter{Account: &acctA, IncludeSystem: true}))
	if len(rows) != 3 {
		t.Fatalf("include system: %+v", rows)
	}
	rows, _ = must2(s.UnreadByConversation(ctx, Filter{Account: &acctB}))
	if len(rows) != 0 {
		t.Fatalf("other account: %+v", rows)
	}
}

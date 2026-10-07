package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSearchFTS(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "c", "m1", "we will deploy the service tonight", base),
		msg(acctA, "c", "m2", "deployment finished, service is green", base.Add(time.Minute)),
		msg(acctA, "c", "m3", "lunch plans for tonight", base.Add(2*time.Minute)),
		msg(acctA, "c", "m4", "please deploy service now", base.Add(3*time.Minute)),
	}))
	search := func(q string, f Filter) []string {
		t.Helper()
		hits, _ := must2(s.Search(ctx, q, f))
		return ids(hits)
	}
	if got := search("tonight", Filter{}); !eqStrings(got, []string{"m3", "m1"}) {
		t.Fatalf("term newest first: %v", got)
	}
	if got := search(`"deploy the service"`, Filter{}); !eqStrings(got, []string{"m1"}) {
		t.Fatalf("phrase: %v", got)
	}
	if got := search("deplo*", Filter{}); !eqStrings(got, []string{"m4", "m2", "m1"}) {
		t.Fatalf("prefix: %v", got)
	}
	if got := search("deploy service", Filter{}); !eqStrings(got, []string{"m4", "m1"}) {
		t.Fatalf("and: %v", got)
	}
	if got := search("deploy OR AND NEAR(", Filter{}); len(got) != 0 {
		t.Fatalf("operators must be quoted: %v", got)
	}
	hits, trunc := must2(s.Search(ctx, "deplo*", Filter{Limit: 2}))
	if len(hits) != 2 || !trunc || !eqStrings(ids(hits), []string{"m4", "m2"}) {
		t.Fatalf("limit: %v %v", ids(hits), trunc)
	}
	_, trunc = must2(s.Search(ctx, "deplo*", Filter{Limit: 3}))
	if trunc {
		t.Fatal("exactly Limit hits is not truncated")
	}
	if got := search("deplo*", Filter{Since: base.Add(time.Minute), Until: base.Add(2 * time.Minute)}); !eqStrings(got, []string{"m2"}) {
		t.Fatalf("since/until: %v", got)
	}
	if _, _, err := s.Search(ctx, "   ", Filter{}); err == nil {
		t.Fatal("empty query must be an error")
	}
	if _, _, err := s.Search(ctx, `"`, Filter{}); err == nil {
		t.Fatal("empty phrase must be an error")
	}
}

func TestBuildFTSQuery(t *testing.T) {
	cases := map[string]string{
		`deploy`:              `"deploy"`,
		`deplo*`:              `"deplo"*`,
		`a "b c" d*`:          `"a" "b c" "d"*`,
		`"unterminated quote`: `"unterminated quote"`,
		`say"hi"`:             `"say" "hi"`,
		`x"y*`:                `"x" "y*"`,
		`*`:                   ``,
	}
	for in, want := range cases {
		if got := buildFTSQuery(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSearchCoversTextOnly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "c", "Chat", "Quarterly Planning")}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c", "m1", "hello", base)}))
	if hits, _ := must2(s.Search(ctx, "quarterly", Filter{})); len(hits) != 0 {
		t.Fatalf("search must not match titles: %v", ids(hits))
	}
}

func TestFromFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := msg(acctA, "c", "m1", "x one", base)
	a.SenderID, a.SenderName = "8:orgid:alice", "Alice Anderson"
	b := msg(acctA, "c", "m2", "x two", base.Add(time.Minute))
	b.SenderID, b.SenderName = "8:orgid:bob", "Bob Alicea"
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{a, b}))
	got, _ := must2(s.Messages(ctx, Filter{From: "8:orgid:alice"}))
	if !eqStrings(ids(got), []string{"m1"}) {
		t.Fatalf("exact id: %v", ids(got))
	}
	got, _ = must2(s.Messages(ctx, Filter{From: "ALICE"}))
	if !eqStrings(ids(got), []string{"m1", "m2"}) {
		t.Fatalf("substring: %v", ids(got))
	}
	got, _ = must2(s.Search(ctx, "x", Filter{From: "anderson"}))
	if !eqStrings(ids(got), []string{"m1"}) {
		t.Fatalf("search from: %v", ids(got))
	}
}

func TestMessagesChronologicalAndLimit(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	var ms []teamsdesktop.Message
	for i := range 5 {
		ms = append(ms, msg(acctA, "c", fmt.Sprintf("m%d", i), "t", base.Add(time.Duration(i)*time.Minute)))
	}
	must(s.ApplyMessages(ctx, ms))
	got, trunc := must2(s.Messages(ctx, Filter{}))
	if !eqStrings(ids(got), []string{"m0", "m1", "m2", "m3", "m4"}) || trunc {
		t.Fatalf("%v %v", ids(got), trunc)
	}
	got, trunc = must2(s.Messages(ctx, Filter{Limit: 2}))
	if !eqStrings(ids(got), []string{"m3", "m4"}) || !trunc {
		t.Fatalf("newest two, chronological: %v %v", ids(got), trunc)
	}
}

func TestConversationsQueryTitleFTS(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c1 := conv(acctA, "c1", "Chat", "Quarterly Planning")
	c2 := conv(acctA, "c2", "Chat", "Lunch crew")
	c2.LastMessageAt = base
	c3 := conv(acctA, "c3", "Topic", "Planning archive")
	c3.LastMessageAt = base.Add(time.Hour)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{c1, c2, c3}))
	got, _ := must2(s.Conversations(ctx, "", "plann*", Filter{}))
	if len(got) != 2 {
		t.Fatalf("prefix: %+v", got)
	}
	if got[0].ID != "c3" {
		t.Fatalf("newest activity first: %+v", got)
	}
	got, _ = must2(s.Conversations(ctx, "topic", "planning", Filter{}))
	if len(got) != 1 || got[0].ID != "c3" {
		t.Fatalf("kind filter: %+v", got)
	}
	got, _ = must2(s.Conversations(ctx, "", "", Filter{}))
	if len(got) != 3 {
		t.Fatalf("all: %+v", got)
	}
	// Renames replace the indexed title.
	c1.Title, c1.DisplayName = "Roadmap", "Roadmap"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{c1}))
	if got, _ = must2(s.Conversations(ctx, "", "quarterly", Filter{})); len(got) != 0 {
		t.Fatalf("stale title hit: %+v", got)
	}
	if got, _ = must2(s.Conversations(ctx, "", "roadmap", Filter{})); len(got) != 1 {
		t.Fatalf("new title: %+v", got)
	}
}

func TestConversationByExactTitle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "19:abc@thread.v2", "Chat", "Ops Room"), conv(acctA, "19:def@thread.v2", "Chat", "Ops Room Extra")}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "19:abc@thread.v2", "m1", "a", base), msg(acctA, "19:def@thread.v2", "m2", "b", base)}))
	got, _ := must2(s.Messages(ctx, Filter{Conversation: "Ops Room"}))
	if !eqStrings(ids(got), []string{"m1"}) {
		t.Fatalf("exact title: %v", ids(got))
	}
	got, _ = must2(s.Messages(ctx, Filter{Conversation: "19:def@thread.v2"}))
	if !eqStrings(ids(got), []string{"m2"}) {
		t.Fatalf("id: %v", ids(got))
	}
}

func TestPeople(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{
		{TenantID: acctA.TenantID, ID: "8:orgid:1", DisplayName: "Alice", SeenAt: base},
		{TenantID: acctA.TenantID, ID: "8:orgid:2", DisplayName: "Bob", SeenAt: base},
		{TenantID: acctB.TenantID, ID: "8:orgid:3", DisplayName: "Alicia", SeenAt: base},
	}))
	got, _ := must2(s.People(ctx, "ali", Filter{}))
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	got, _ = must2(s.People(ctx, "ali", Filter{Account: &acctA}))
	if len(got) != 1 || got[0].DisplayName != "Alice" {
		t.Fatalf("%+v", got)
	}
	got, _ = must2(s.People(ctx, "8:orgid:2", Filter{}))
	if len(got) != 1 || got[0].DisplayName != "Bob" {
		t.Fatalf("by id: %+v", got)
	}
	// Re-seeing a person later with a new name updates them and keeps first_seen_at.
	c := must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:1", DisplayName: "Alice Smith", SeenAt: base.Add(time.Hour)}}))
	if c.Updated != 1 {
		t.Fatalf("%+v", c)
	}
	got, _ = must2(s.People(ctx, "smith", Filter{}))
	if len(got) != 1 || !got[0].FirstSeenAt.Equal(base) || !got[0].LastSeenAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("%+v", got)
	}
}

func TestKeepsEvictedMessages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := []teamsdesktop.Message{msg(acctA, "c", "m1", "old one", base), msg(acctA, "c", "m2", "old two", base.Add(time.Minute)), msg(acctA, "c", "m3", "kept", base.Add(2*time.Minute))}
	must(s.ApplyMessages(ctx, first))
	// Teams evicts m1 and m2 from its cache; the next sync only sees m3 plus a new one.
	second := []teamsdesktop.Message{first[2], msg(acctA, "c", "m4", "fresh", base.Add(3*time.Minute))}
	c := must(s.ApplyMessages(ctx, second))
	if c != (Counts{Seen: 2, Inserted: 1, Unchanged: 1}) {
		t.Fatalf("%+v", c)
	}
	got, _ := must2(s.Messages(ctx, Filter{}))
	if !eqStrings(ids(got), []string{"m1", "m2", "m3", "m4"}) {
		t.Fatalf("evicted messages lost: %v", ids(got))
	}
	if hits, _ := must2(s.Search(ctx, "old", Filter{})); len(hits) != 2 {
		t.Fatalf("evicted messages not searchable: %v", ids(hits))
	}
}

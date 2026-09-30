package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

func TestChannelDisplayNameComposed(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	team := conv(acctA, "19:team@thread.tacv2", "Space", "Platform Team")
	ch := conv(acctA, "19:chan@thread.tacv2", "Topic", "Releases")
	ch.TeamID = team.ID
	orphan := conv(acctA, "19:orphan@thread.tacv2", "Topic", "Lonely")
	orphan.TeamID = "19:missing@thread.tacv2"
	chat := conv(acctA, "19:chat@unq.gbl.spaces", "Chat", "Pat, Sam")
	// The same team id under another account must not leak into this channel's name.
	otherTeam := conv(acctB, team.ID, "Space", "Wrong Team")
	// The channel arrives before the team record: composition happens at query time.
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{ch, orphan, chat, otherTeam}))
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, ch.ID, "m1", "ship it", base), msg(acctA, chat.ID, "m2", "hi", base), msg(acctA, orphan.ID, "m3", "x", base),
	}))
	want := map[string]string{"m1": "Platform Team › Releases", "m2": "Pat, Sam", "m3": "Lonely"}
	rows, _ := must2(s.Messages(ctx, Filter{}))
	for _, r := range rows {
		if r.ConversationDisplayName != want[r.ID] {
			t.Errorf("%s: %q want %q", r.ID, r.ConversationDisplayName, want[r.ID])
		}
	}
	convs, _ := must2(s.Conversations(ctx, "", "", Filter{Account: &acctA}))
	got := map[string]string{}
	for _, c := range convs {
		got[c.ID] = c.DisplayName
	}
	if got[ch.ID] != "Platform Team › Releases" || got[team.ID] != "Platform Team" {
		t.Fatalf("conversations: %v", got)
	}
	// Filtering by the composed name works.
	byName, _ := must2(s.Messages(ctx, Filter{Conversation: "Platform Team › Releases"}))
	if !eqStrings(ids(byName), []string{"m1"}) {
		t.Fatalf("by composed name: %v", ids(byName))
	}
}

func TestActivityJoin(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	team := conv(acctA, "team1", "Space", "Platform Team")
	ch := conv(acctA, "chan1", "Topic", "Releases")
	ch.TeamID = "team1"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team, ch}))
	m := msg(acctA, "chan1", "m1", "@you please review", base)
	m.SenderName = "Dana Dev"
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m}))
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base.Add(time.Minute), ConversationID: "chan1", MessageID: "m1", ReplyChainID: "m1"},
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a2", Type: "reaction", At: base.Add(2 * time.Minute), ConversationID: "gone", MessageID: "nope", IsRead: true},
		{TenantID: acctB.TenantID, UserID: acctB.UserID, ID: "a1", Type: "mention", At: base},
	}))
	rows, trunc, err := s.Activity(ctx, ActivityFilter{Account: &acctA})
	if err != nil || trunc || len(rows) != 2 {
		t.Fatalf("%v %v %+v", err, trunc, rows)
	}
	if rows[0].ID != "a2" || rows[1].ID != "a1" {
		t.Fatalf("newest first: %s, %s", rows[0].ID, rows[1].ID)
	}
	a1 := rows[1]
	if a1.MessageText != "@you please review" || a1.SenderName != "Dana Dev" || a1.ConversationDisplayName != "Platform Team › Releases" || a1.Type != "mention" || a1.IsRead {
		t.Fatalf("join: %+v", a1)
	}
	// An item whose message is not in the archive still lists.
	if rows[0].MessageText != "" || rows[0].ConversationDisplayName != "" || !rows[0].IsRead {
		t.Fatalf("orphan: %+v", rows[0])
	}
	all, _, _ := s.Activity(ctx, ActivityFilter{})
	if len(all) != 3 {
		t.Fatalf("all accounts: %d", len(all))
	}
}

func TestActivityUnreadFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	var acts []teamsdesktop.Activity
	for i, tc := range []struct {
		typ  string
		read bool
	}{{"mention", false}, {"reply", false}, {"mention", true}, {"reaction", false}} {
		acts = append(acts, teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: string(rune('a' + i)), Type: tc.typ, IsRead: tc.read, At: base.Add(time.Duration(i) * time.Hour)})
	}
	must(s.ApplyActivity(ctx, acts))
	ids := func(f ActivityFilter) string {
		rows, _, err := s.Activity(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, r := range rows {
			out += r.ID
		}
		return out
	}
	if got := ids(ActivityFilter{Unread: true}); got != "dba" {
		t.Fatalf("unread: %s", got)
	}
	if got := ids(ActivityFilter{Unread: true, Type: "Mention"}); got != "a" {
		t.Fatalf("unread mention: %s", got)
	}
	if got := ids(ActivityFilter{Since: base.Add(90 * time.Minute)}); got != "dc" {
		t.Fatalf("since: %s", got)
	}
	rows, trunc, _ := s.Activity(ctx, ActivityFilter{Limit: 2})
	if len(rows) != 2 || !trunc {
		t.Fatalf("limit: %d %v", len(rows), trunc)
	}
	// Marking read later updates the row.
	acts[0].IsRead = true
	c := must(s.ApplyActivity(ctx, acts[:1]))
	if c.Updated != 1 || ids(ActivityFilter{Unread: true}) != "db" {
		t.Fatalf("%+v", c)
	}
}

func TestUnread(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c1 := conv(acctA, "c1", "Chat", "Read up to m2")
	c1.ReadHorizonAt = base.Add(2 * time.Minute)
	c1.ReadHorizonMessageID = "m2"
	c2 := conv(acctA, "c2", "Chat", "Nothing read")
	c2.ReadHorizonAt = base
	c3 := conv(acctA, "c3", "Chat", "Horizon unknown")
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{c1, c2, c3}))

	own := msg(acctA, "c1", "m5", "my own reply", base.Add(5*time.Minute))
	own.SenderID = selfMRI(acctA)
	deleted := msg(acctA, "c1", "m6", "deleted later", base.Add(6*time.Minute))
	deleted.DeletedAt = base.Add(7 * time.Minute)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "c1", "m1", "old", base.Add(1*time.Minute)),
		msg(acctA, "c1", "m2", "at horizon", base.Add(2*time.Minute)),
		msg(acctA, "c1", "m3", "new one", base.Add(3*time.Minute)),
		msg(acctA, "c1", "m4", "new two", base.Add(4*time.Minute)),
		own, deleted,
		msg(acctA, "c2", "n1", "unread elsewhere", base.Add(time.Minute)),
		msg(acctA, "c3", "x1", "no horizon", base.Add(time.Minute)),
		// Same id space under another account: its own user differs, horizon absent.
		msg(acctB, "c1", "m3", "other account", base.Add(3*time.Minute)),
	}))
	rows, trunc := must2(s.Unread(ctx, Filter{Account: &acctA}))
	if trunc || !eqStrings(ids(rows), []string{"m4", "m3", "n1"}) {
		t.Fatalf("unread: %v %v", ids(rows), trunc)
	}
	rows, _ = must2(s.Unread(ctx, Filter{Account: &acctA, Conversation: "c1"}))
	if !eqStrings(ids(rows), []string{"m4", "m3"}) {
		t.Fatalf("unread in c1: %v", ids(rows))
	}
	// Deleted messages stay out even when the caller asks for deleted ones elsewhere.
	rows, _ = must2(s.Unread(ctx, Filter{Account: &acctA, IncludeDeleted: true, Conversation: "c1"}))
	if !eqStrings(ids(rows), []string{"m4", "m3"}) {
		t.Fatalf("deleted counted unread: %v", ids(rows))
	}
	// Filter.Unread on Messages applies the same predicate.
	rows, _ = must2(s.Messages(ctx, Filter{Account: &acctA, Unread: true}))
	if !eqStrings(ids(rows), []string{"n1", "m3", "m4"}) {
		t.Fatalf("messages --unread: %v", ids(rows))
	}
	rows, _ = must2(s.Search(ctx, "new", Filter{Account: &acctA, Unread: true}))
	if !eqStrings(ids(rows), []string{"m4", "m3"}) {
		t.Fatalf("search --unread: %v", ids(rows))
	}
	rows, _ = must2(s.Unread(ctx, Filter{Account: &acctA, Limit: 1}))
	if len(rows) != 1 {
		t.Fatalf("limit: %v", ids(rows))
	}
}

func TestThread(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := msg(acctA, "chan", "r1", "root post", base)
	root.ReplyChainID = "r1"
	var ms []teamsdesktop.Message
	ms = append(ms, root)
	for i, id := range []string{"r3", "r2", "r4"} {
		m := msg(acctA, "chan", id, "reply "+id, base.Add(time.Duration(i+1)*time.Minute))
		m.ReplyChainID, m.ParentMessageID = "r1", "r1"
		ms = append(ms, m)
	}
	other := msg(acctA, "chan", "z1", "different thread", base.Add(time.Minute))
	other.ReplyChainID = "z1"
	otherAcct := msg(acctB, "chan", "q1", "same ids other account", base)
	otherAcct.ReplyChainID, otherAcct.ParentMessageID = "r1", "r1"
	gone := msg(acctA, "chan", "r5", "deleted reply", base.Add(time.Hour))
	gone.ReplyChainID, gone.ParentMessageID, gone.DeletedAt = "r1", "r1", base.Add(2*time.Hour)
	ms = append(ms, other, otherAcct, gone)
	must(s.ApplyMessages(ctx, ms))

	rows, err := s.Thread(ctx, "chan", "r1", Filter{Account: &acctA})
	if err != nil {
		t.Fatal(err)
	}
	if !eqStrings(ids(rows), []string{"r1", "r3", "r2", "r4"}) {
		t.Fatalf("thread order: %v", ids(rows))
	}
	rows, _ = s.Thread(ctx, "chan", "r1", Filter{Account: &acctA, IncludeDeleted: true})
	if len(rows) != 5 {
		t.Fatalf("include deleted: %v", ids(rows))
	}
	rows, _ = s.Thread(ctx, "chan", "r1", Filter{})
	if len(rows) != 5 {
		t.Fatalf("both accounts: %v", ids(rows))
	}
}

func TestWhoami(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.ApplyAccount(ctx, acctB))
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "Me Myself", SeenAt: base}}))
	who, err := s.Whoami(ctx)
	if err != nil || len(who) != 2 {
		t.Fatalf("%v %+v", err, who)
	}
	var a, b WhoamiRow
	for _, w := range who {
		if w.TenantID == acctA.TenantID {
			a = w
		} else {
			b = w
		}
	}
	if a.UserID != acctA.UserID || a.SelfID != selfMRI(acctA) || a.DisplayName != "Me Myself" || a.Locale != "en-us" || a.LastSyncedAt.IsZero() {
		t.Fatalf("A: %+v", a)
	}
	if b.DisplayName != "" || b.SelfID != selfMRI(acctB) {
		t.Fatalf("B: %+v", b)
	}
}

func TestMentionsMeFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m1 := msg(acctA, "c", "m1", "deploy ping", base)
	m1.MentionsMe = true
	m1.Mentions = []teamsdesktop.Mention{{ID: selfMRI(acctA), DisplayName: "Me"}}
	m2 := msg(acctA, "c", "m2", "deploy quiet", base.Add(time.Minute))
	m2.Mentions = []teamsdesktop.Mention{{ID: "8:orgid:other", DisplayName: "Other"}}
	m2.Reactions = []teamsdesktop.Reaction{{Key: "like", Count: 2, UserIDs: []string{"a", "b"}}}
	m2.Files = []teamsdesktop.File{{Name: "a.pdf", URL: "https://example.test/a.pdf", Type: "pdf"}}
	m2.Links = []string{"https://example.test/x"}
	m2.Subject, m2.Importance, m2.Pinned, m2.Link = "Subj", "high", true, "https://teams.microsoft.com/l/message/c/m2"
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m1, m2}))
	got, _ := must2(s.Messages(ctx, Filter{MentionsMe: true}))
	if !eqStrings(ids(got), []string{"m1"}) || !got[0].MentionsMe || len(got[0].Mentions) != 1 {
		t.Fatalf("messages: %+v", got)
	}
	got, _ = must2(s.Search(ctx, "deploy", Filter{MentionsMe: true}))
	if !eqStrings(ids(got), []string{"m1"}) {
		t.Fatalf("search: %v", ids(got))
	}
	all, _ := must2(s.Messages(ctx, Filter{}))
	r := all[1]
	if r.ID != "m2" || len(r.Reactions) != 1 || r.Reactions[0].Count != 2 || len(r.Files) != 1 || r.Files[0].Name != "a.pdf" ||
		len(r.Links) != 1 || r.Subject != "Subj" || r.Importance != "high" || !r.Pinned || r.Link == "" || r.MentionsMe {
		t.Fatalf("enrichment round trip: %+v", r)
	}
}

func TestApplyMessages50kTiming(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	n := 50000
	if raceEnabled {
		n = 5000 // the race detector is ~15x slower; the 50k timing runs in the plain build
	}
	ms := make([]teamsdesktop.Message, n)
	for i := range ms {
		m := msg(acctA, "conv-"+string(rune('a'+i%26)), "id-"+itoa(i), "message number "+itoa(i)+" about deployment and lunch plans with some more words to index", base.Add(time.Duration(i)*time.Second))
		m.Raw = make([]byte, 1500)
		m.ContentHTML = "<p>" + m.ContentText + "</p>"
		m.Reactions = []teamsdesktop.Reaction{{Key: "like", Count: 1, UserIDs: []string{"8:orgid:x"}}}
		ms[i] = m
	}
	start := time.Now()
	c := must(s.ApplyMessages(ctx, ms))
	first := time.Since(start)
	start = time.Now()
	c2 := must(s.ApplyMessages(ctx, ms))
	second := time.Since(start)
	t.Logf("apply %d messages: insert %v, idempotent re-apply %v", n, first, second)
	if c.Inserted != n || c2.Unchanged != n {
		t.Fatalf("%+v %+v", c, c2)
	}
	if first > 30*time.Second {
		t.Fatalf("too slow: %v (whole sync budget is 60s)", first)
	}
	start = time.Now()
	hits, _ := must2(s.Search(ctx, "deployment", Filter{Limit: 50}))
	t.Logf("search over 50k: %v (%d hits)", time.Since(start), len(hits))
}

func itoa(i int) string { return fmtInt(i) }

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

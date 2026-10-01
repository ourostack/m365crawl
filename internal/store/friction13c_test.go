package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

func mentionAct(id, subtype, conv, msgID string, at time.Time) teamsdesktop.Activity {
	return teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: id, Type: "mention", Subtype: subtype, At: at, ConversationID: conv, MessageID: msgID}
}

func TestMentionKindAndDirectMentions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	person := msg(acctA, "c", "person", "ping", base)
	person.MentionsMe = true
	// Both a person mention by id and a team broadcast: the person mention wins.
	both := msg(acctA, "c", "both", "ping", base.Add(time.Minute))
	both.MentionsMe = true
	// A person mention only the activity feed knows about.
	feedPerson := msg(acctA, "c", "feedperson", "ping", base.Add(2*time.Minute))
	var ms []teamsdesktop.Message
	ms = append(ms, person, both, feedPerson)
	for i, kind := range []string{"channel", "team", "tag", "everyone", "weird", ""} {
		ms = append(ms, msg(acctA, "c", "k-"+kind, "ping", base.Add(time.Duration(10+i)*time.Minute)))
	}
	ms = append(ms, msg(acctA, "c", "plain", "ping", base.Add(time.Hour)))
	must(s.ApplyMessages(ctx, ms))
	acts := []teamsdesktop.Activity{
		mentionAct("a-both", "team", "c", "both", base),
		mentionAct("a-feedperson", "Person", "c", "feedperson", base),
	}
	for _, kind := range []string{"channel", "team", "tag", "everyone", "weird", ""} {
		a := mentionAct("a-"+kind, kind, "c", "k-"+kind, base)
		a.Type = "mentionInChat"
		acts = append(acts, a)
	}
	must(s.ApplyActivity(ctx, acts))

	rows, _ := must2(s.Messages(ctx, Filter{Limit: 50}))
	kind := map[string]string{}
	for _, r := range rows {
		kind[r.ID] = r.MentionKind
	}
	want := map[string]string{"person": "person", "both": "person", "feedperson": "person", "k-channel": "channel", "k-team": "team", "k-tag": "tag", "k-everyone": "everyone", "k-weird": "other", "k-": "other", "plain": ""}
	for id, w := range want {
		if kind[id] != w {
			t.Errorf("mention kind of %s = %q, want %q", id, kind[id], w)
		}
	}
	mine, _ := must2(s.Messages(ctx, Filter{MentionsMe: true}))
	if len(mine) != 9 {
		t.Errorf("mentions me = %v", ids(mine))
	}
	direct, _ := must2(s.Messages(ctx, Filter{DirectMentions: true}))
	if !eqStrings(ids(direct), []string{"person", "both", "feedperson"}) {
		t.Errorf("direct mentions = %v", ids(direct))
	}
	found, _ := must2(s.Search(ctx, "ping", Filter{DirectMentions: true}))
	if len(found) != 3 {
		t.Errorf("search direct mentions = %v", ids(found))
	}
	// A direct-mentions filter alone is enough for a search without words.
	bare, _ := must2(s.Search(ctx, "", Filter{DirectMentions: true}))
	if len(bare) != 3 {
		t.Errorf("bare search direct mentions = %v", ids(bare))
	}
}

func TestActivityDirectMentions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	acts := []teamsdesktop.Activity{
		mentionAct("direct", "person", "", "", base),
		mentionAct("team", "team", "", "", base.Add(time.Minute)),
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "chat", Type: "mentionInChat", Subtype: "person", At: base.Add(2 * time.Minute)},
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "reply", Type: "reply", Subtype: "person", At: base.Add(3 * time.Minute)},
	}
	must(s.ApplyActivity(ctx, acts))
	rows, _, err := s.Activity(ctx, ActivityFilter{DirectMentions: true})
	if err != nil || len(rows) != 2 || rows[0].ID != "chat" || rows[1].ID != "direct" {
		t.Fatalf("%v %+v", err, rows)
	}
}

func TestActivityActor(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	me := selfMRI(acctA)
	mine := msg(acctA, "c", "mine", "my post", base)
	mine.SenderID, mine.SenderName = me, "Me"
	mine.Raw = []byte(`{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:me-not","time":1},{"mri":"8:orgid:rea","time":` + millis(base.Add(time.Minute)) + `}]},{"key":"heart","users":[{"mri":"` + me + `","time":5}]}]}}`)
	other := msg(acctA, "c", "other", "their post", base.Add(time.Hour))
	other.SenderID, other.SenderName = "8:orgid:snd", "Sam Sender"
	unknown := msg(acctA, "c", "unknown", "anon", base.Add(2*time.Hour))
	unknown.SenderID, unknown.SenderName = "8:orgid:ghost", ""
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{mine, other, unknown}))
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:rea", DisplayName: "Rae Reactor", SeenAt: base}}))
	a := func(id, typ, subtype, msgID string, min int) teamsdesktop.Activity {
		return teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: id, Type: typ, Subtype: subtype, At: base.Add(time.Duration(min) * time.Minute), ConversationID: "c", MessageID: msgID}
	}
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{
		a("react", "reactionInChat", "like", "mine", 1),
		a("selfonly", "reaction", "heart", "mine", 2),
		a("noreactor", "reaction", "laugh", "mine", 3),
		a("reaction-no-message", "reaction", "like", "gone", 4),
		a("ment", "mention", "channel", "other", 5),
		a("chat", "mentionInChat", "person", "other", 6),
		a("rep", "reply", "", "other", 7),
		a("rr", "replyToReply", "", "other", 8),
		a("fol", "follow", "channelNewMessage", "other", 9),
		a("graph", "msGraph", "approvalCreated", "other", 10),
		a("ghost", "mention", "person", "unknown", 11),
	}))
	rows, _, err := s.Activity(ctx, ActivityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, r := range rows {
		got[r.ID] = [2]string{r.ActorID, r.ActorName}
	}
	want := map[string][2]string{
		"react": {"8:orgid:rea", "Rae Reactor"}, "selfonly": {}, "noreactor": {}, "reaction-no-message": {},
		"ment": {"8:orgid:snd", "Sam Sender"}, "chat": {"8:orgid:snd", "Sam Sender"}, "rep": {"8:orgid:snd", "Sam Sender"},
		"rr": {"8:orgid:snd", "Sam Sender"}, "fol": {"8:orgid:snd", "Sam Sender"}, "graph": {},
		"ghost": {"8:orgid:ghost", ""},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("actor of %s = %v, want %v", id, got[id], w)
		}
	}
	// The sender of a reaction's message is still the message's author, not the reactor.
	for _, r := range rows {
		if r.ID == "react" && r.SenderID != me {
			t.Errorf("sender of the reacted message = %q", r.SenderID)
		}
	}
}

func millis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func TestTeams(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mk := func(a teamsdesktop.Account, id, kind, name, team string, last time.Time, horizon time.Time) teamsdesktop.Conversation {
		c := conv(a, id, kind, name)
		c.TeamID, c.LastMessageAt, c.ReadHorizonAt = team, last, horizon
		return c
	}
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{
		mk(acctA, "t1", "Space", "Platform", "t1", base, time.Time{}),
		mk(acctA, "t1-a", "Topic", "General", "t1", base.Add(3*time.Hour), base.Add(time.Minute)),
		mk(acctA, "t1-b", "Topic", "Releases", "t1", base.Add(time.Hour), base.Add(time.Minute)),
		mk(acctA, "t2", "Space", "Design", "t2", base.Add(2*time.Hour), time.Time{}),
		mk(acctA, "t2-a", "Topic", "Reviews", "t2", time.Time{}, time.Time{}),
		mk(acctA, "t3", "Space", "Childless", "t3", base.Add(9*time.Hour), time.Time{}), // no channels archived: not listed
		mk(acctA, "chat", "Chat", "A chat", "", base.Add(10*time.Hour), base),
		mk(acctB, "t4", "Space", "Other account", "t4", base.Add(4*time.Hour), time.Time{}),
		mk(acctB, "t4-a", "Topic", "Elsewhere", "t4", base.Add(4*time.Hour), base),
	}))
	own := msg(acctA, "t1-a", "own", "mine", base.Add(2*time.Hour))
	own.SenderID = selfMRI(acctA)
	deleted := msg(acctA, "t1-a", "del", "gone", base.Add(2*time.Hour))
	deleted.DeletedAt = base.Add(3 * time.Hour)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{
		msg(acctA, "t1-a", "old", "read", base),
		msg(acctA, "t1-a", "n1", "unread", base.Add(2*time.Minute)),
		msg(acctA, "t1-a", "n2", "unread", base.Add(3*time.Minute)),
		msg(acctA, "t1-b", "n3", "unread", base.Add(4*time.Minute)),
		msg(acctA, "t1", "n4", "no horizon in the team's own conversation", base.Add(4*time.Minute)),
		msg(acctA, "chat", "c1", "a chat is not a team", base.Add(4*time.Minute)),
		own, deleted,
		msg(acctB, "t4-a", "b1", "unread", base.Add(time.Minute)),
	}))

	rows, trunc, err := s.Teams(ctx, Filter{})
	if err != nil || trunc || len(rows) != 3 {
		t.Fatalf("%v %v %+v", err, trunc, rows)
	}
	t4, t1, t2 := rows[0], rows[1], rows[2]
	if t4.ID != "t4" || t4.DisplayName != "Other account" || t4.ChannelCount != 1 || t4.UnreadCount != 1 || !t4.LastActivityAt.Equal(base.Add(4*time.Hour)) || t4.TenantID != acctB.TenantID || t4.UserID != acctB.UserID {
		t.Errorf("t4 = %+v", t4)
	}
	// Newest channel activity counts as the team's, not the team conversation's own time.
	if t1.ID != "t1" || t1.DisplayName != "Platform" || t1.ChannelCount != 2 || t1.UnreadCount != 3 || !t1.LastActivityAt.Equal(base.Add(3*time.Hour)) {
		t.Errorf("t1 = %+v", t1)
	}
	if t2.ID != "t2" || t2.ChannelCount != 1 || t2.UnreadCount != 0 || !t2.LastActivityAt.Equal(base.Add(2*time.Hour)) {
		t.Errorf("t2 = %+v", t2)
	}

	one, _, _ := s.Teams(ctx, Filter{Account: &acctA})
	if len(one) != 2 || one[0].ID != "t1" {
		t.Errorf("account filter: %+v", one)
	}
	var total int
	cut, trunc, err := s.Teams(ctx, Filter{Limit: 2, Total: &total})
	if err != nil || !trunc || len(cut) != 2 || total != 3 {
		t.Errorf("limit: %v %v %d total %d", err, trunc, len(cut), total)
	}
}

// seedActors is an archive where Teams and the actor lookups each have something to read: a team
// with a channel and unread messages, a reaction item on a message with a named reactor, and a
// mention item.
func seedActors(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	team := conv(acctA, "team", "Space", "Team")
	team.TeamID = "team"
	ch := conv(acctA, "chan", "Topic", "Channel")
	ch.TeamID, ch.ReadHorizonAt, ch.LastMessageAt = "team", base, base.Add(time.Hour)
	team2 := conv(acctA, "team2", "Space", "Second team")
	ch2 := conv(acctA, "chan2", "Topic", "Second channel")
	ch2.TeamID = "team2"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team, ch, team2, ch2}))
	mine := msg(acctA, "chan", "mine", "mine", base.Add(time.Minute))
	mine.SenderID = selfMRI(acctA)
	mine.Raw = []byte(`{"properties":{"emotions":[{"key":"like","users":[{"mri":"8:orgid:rea","time":` + millis(base) + `}]}]}}`)
	other := msg(acctA, "chan", "other", "other", base.Add(2*time.Minute))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{mine, other}))
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:rea", DisplayName: "Rae", SeenAt: base}}))
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{
		{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "react", Type: "reaction", Subtype: "like", At: base, ConversationID: "chan", MessageID: "mine"},
		mentionAct("ment", "channel", "chan", "other", base),
	}))
}

// TestFriction13cReadFailuresSurface fails every driver call of the new and changed reads.
func TestFriction13cReadFailuresSurface(t *testing.T) {
	ops := map[string]func(context.Context, *Store) error{
		"Teams": func(ctx context.Context, s *Store) error {
			_, _, err := s.Teams(ctx, Filter{Account: &acctA})
			return err
		},
		"Teams cut at the limit": func(ctx context.Context, s *Store) error {
			var n int
			_, _, err := s.Teams(ctx, Filter{Limit: 1, Total: &n})
			return err
		},
		"Activity with actors": func(ctx context.Context, s *Store) error { _, _, err := s.Activity(ctx, ActivityFilter{}); return err },
		"Activity direct mention": func(ctx context.Context, s *Store) error {
			_, _, err := s.Activity(ctx, ActivityFilter{DirectMentions: true})
			return err
		},
		"Messages with kinds": func(ctx context.Context, s *Store) error {
			_, _, err := s.Messages(ctx, Filter{DirectMentions: true})
			return err
		},
	}
	nullSafe := map[string][]int{"Activity with actors": {3}}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, seedActors, op, nullSafe[name]...) })
	}
}

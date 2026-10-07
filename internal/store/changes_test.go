package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func key(a teamsdesktop.Account, conv, id string) string {
	return fmt.Sprintf("%s|%s|%s|%s", a.TenantID, a.UserID, conv, id)
}

func TestApplyMessagesChanges(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m1 := msg(acctA, "c1", "m1", "hello", base)
	m2 := msg(acctA, "c1", "m2", "second", base.Add(time.Minute))
	c, changes, err := s.ApplyMessagesChanges(ctx, []teamsdesktop.Message{m1, m2})
	if err != nil || c.Inserted != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	want := []Change{{Change: ChangeNew, Key: key(acctA, "c1", "m1")}, {Change: ChangeNew, Key: key(acctA, "c1", "m2")}}
	if fmt.Sprint(changes) != fmt.Sprint(want) {
		t.Fatalf("new: %v want %v", changes, want)
	}
	// Re-apply: nothing changed, no changes reported.
	c, changes, _ = s.ApplyMessagesChanges(ctx, []teamsdesktop.Message{m1, m2})
	if c.Unchanged != 2 || len(changes) != 0 {
		t.Fatalf("idempotent: %+v %v", c, changes)
	}
	// An edit (newer version) and a deletion (tombstone arrives).
	m1.Version, m1.ContentText, m1.EditedAt = 2, "hello edited", base.Add(time.Hour)
	m2.Version, m2.DeletedAt = 2, base.Add(time.Hour)
	c, changes, _ = s.ApplyMessagesChanges(ctx, []teamsdesktop.Message{m1, m2})
	want = []Change{{Change: ChangeEdited, Key: key(acctA, "c1", "m1")}, {Change: ChangeDeleted, Key: key(acctA, "c1", "m2")}}
	if c.Updated != 2 || fmt.Sprint(changes) != fmt.Sprint(want) {
		t.Fatalf("edit/delete: %+v %v want %v", c, changes, want)
	}
	// A deleted row that changes again is not "deleted" a second time.
	m2.Version = 3
	m2.ContentText = "still gone"
	_, changes, _ = s.ApplyMessagesChanges(ctx, []teamsdesktop.Message{m2})
	if len(changes) != 1 || changes[0].Change != ChangeEdited {
		t.Fatalf("second change: %v", changes)
	}
	// ApplyMessages still returns the same Counts.
	if c2, err := s.ApplyMessages(ctx, []teamsdesktop.Message{m1}); err != nil || c2.Unchanged != 1 {
		t.Fatalf("%+v %v", c2, err)
	}
}

func TestApplyActivityChanges(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base, Raw: []byte(`{}`)}
	_, changes, err := s.ApplyActivityChanges(ctx, []teamsdesktop.Activity{a})
	if err != nil || len(changes) != 1 || changes[0] != (Change{Change: ChangeNew, Key: fmt.Sprintf("%s|%s|a1", acctA.TenantID, acctA.UserID)}) {
		t.Fatalf("%v %v", changes, err)
	}
	a.IsRead = true
	_, changes, _ = s.ApplyActivityChanges(ctx, []teamsdesktop.Activity{a})
	if len(changes) != 1 || changes[0].Change != ChangeEdited {
		t.Fatalf("read flip: %v", changes)
	}
	_, changes, _ = s.ApplyActivityChanges(ctx, []teamsdesktop.Activity{a})
	if len(changes) != 0 {
		t.Fatalf("unchanged: %v", changes)
	}
}

func TestDeletedAtSticky(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := msg(acctA, "c1", "m1", "bye", base)
	m.Version, m.DeletedAt = 5, base.Add(time.Hour)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m}))
	// The cache copy loses the tombstone at the same version (content differs too).
	lost := m
	lost.DeletedAt, lost.ContentText = time.Time{}, "bye (no tombstone)"
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{lost}))
	rows, _ := must2(s.Messages(ctx, Filter{IncludeDeleted: true}))
	if len(rows) != 1 || rows[0].DeletedAt.IsZero() || !rows[0].DeletedAt.Equal(m.DeletedAt) {
		t.Fatalf("deleted_at cleared: %+v", rows)
	}
	if rows[0].ContentText != "bye (no tombstone)" {
		t.Fatalf("other fields should still update: %q", rows[0].ContentText)
	}
	live, _ := must2(s.Messages(ctx, Filter{}))
	if len(live) != 0 {
		t.Fatalf("message came back: %v", ids(live))
	}
	// Re-applying the same tombstone-less copy is idempotent.
	c := must(s.ApplyMessages(ctx, []teamsdesktop.Message{lost}))
	if c.Unchanged != 1 {
		t.Fatalf("not idempotent: %+v", c)
	}
	// A newer version without a tombstone (an undelete) may clear it.
	lost.Version = 6
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{lost}))
	live, _ = must2(s.Messages(ctx, Filter{}))
	if len(live) != 1 {
		t.Fatalf("newer version should clear the tombstone: %v", ids(live))
	}
}

func TestConversationHorizonNotCleared(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c := conv(acctA, "c1", "Chat", "Pat")
	c.ReadHorizonAt, c.ReadHorizonClientMessageID = base, "client-7"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{c}))
	lost := c
	lost.ReadHorizonAt, lost.ReadHorizonClientMessageID = time.Time{}, ""
	lost.Title = "Pat renamed"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{lost}))
	rows, _ := must2(s.Conversations(ctx, "", "", Filter{}))
	if len(rows) != 1 || !rows[0].ReadHorizonAt.Equal(base) || rows[0].Title != "Pat renamed" {
		t.Fatalf("%+v", rows)
	}
	var cid string
	if err := s.db.QueryRowContext(ctx, `select read_horizon_client_message_id from conversations where id='c1'`).Scan(&cid); err != nil || cid != "client-7" {
		t.Fatalf("client id %q %v", cid, err)
	}
	if n := must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{lost})); n.Unchanged != 1 {
		t.Fatalf("not idempotent: %+v", n)
	}
	// A new non-null horizon replaces the old one.
	lost.ReadHorizonAt = base.Add(time.Hour)
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{lost}))
	rows, _ = must2(s.Conversations(ctx, "", "", Filter{}))
	if !rows[0].ReadHorizonAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("horizon not advanced: %v", rows[0].ReadHorizonAt)
	}
}

func TestFromProbeScopedToAccount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := msg(acctA, "c1", "m1", "from a", base)
	a.SenderID, a.SenderName = "8:orgid:a-sender", "Sam"
	// Account B has a sender whose id equals the text "Sam"; it must not flip A's filter to exact mode.
	b := msg(acctB, "c1", "m2", "from b", base)
	b.SenderID, b.SenderName = "Sam", "Someone Else"
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{a, b}))
	rows, _ := must2(s.Messages(ctx, Filter{Account: &acctA, From: "Sam"}))
	if !eqStrings(ids(rows), []string{"m1"}) {
		t.Fatalf("substring match by name in account A: %v", ids(rows))
	}
	rows, _ = must2(s.Messages(ctx, Filter{Account: &acctB, From: "Sam"}))
	if !eqStrings(ids(rows), []string{"m2"}) {
		t.Fatalf("exact id in account B: %v", ids(rows))
	}
}

func TestChannelFindableByTeamName(t *testing.T) {
	for _, order := range []string{"team first", "channel first", "same batch", "rename"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			team := conv(acctA, "19:team@thread.tacv2", "Space", "Platform Team")
			ch := conv(acctA, "19:chan@thread.tacv2", "Topic", "Releases")
			ch.TeamID = team.ID
			other := conv(acctA, "19:other@thread.tacv2", "Topic", "Elsewhere")
			// The same team id under another account must not make this account's channels match.
			wrong := conv(acctB, team.ID, "Space", "Wrong Squad")
			must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{wrong, other}))
			switch order {
			case "team first":
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team}))
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{ch}))
			case "channel first":
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{ch}))
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team}))
			case "same batch":
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{ch, team}))
			case "rename":
				old := team
				old.Title, old.DisplayName = "Old Name", "Old Name"
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{old, ch}))
				must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team}))
			}
			find := func(q string, acct teamsdesktop.Account) []string {
				rows, _ := must2(s.Conversations(ctx, "", q, Filter{Account: &acct}))
				var out []string
				for _, r := range rows {
					if r.Kind == "Topic" {
						out = append(out, r.DisplayName)
					}
				}
				return out
			}
			if got := find("platform", acctA); !eqStrings(got, []string{"Platform Team › Releases"}) {
				t.Fatalf("platform: %v", got)
			}
			if got := find("platform releases", acctA); !eqStrings(got, []string{"Platform Team › Releases"}) {
				t.Fatalf("platform releases: %v", got)
			}
			if got := find("releases", acctA); len(got) != 1 {
				t.Fatalf("releases: %v", got)
			}
			if got := find("squad", acctA); len(got) != 0 {
				t.Fatalf("other account's team name leaked: %v", got)
			}
			if order == "rename" {
				if got := find("old", acctA); len(got) != 0 {
					t.Fatalf("old team name still indexed: %v", got)
				}
			}
		})
	}
}

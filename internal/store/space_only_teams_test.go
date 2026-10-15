package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSpaceOnlyTeamInventoryAndReads(t *testing.T) {
	for _, tc := range []struct {
		name, kind, teamID string
	}{
		{"self-referencing", "Space", "19:root@thread.tacv2"},
		{"without-team-id", "sPaCe", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			root := conv(acctA, "19:root@thread.tacv2", tc.kind, "ExampleTeam")
			root.TeamID, root.LastMessageAt = tc.teamID, base
			chat := conv(acctA, "chat", "Chat", "Ordinary chat")
			chat.TeamID = chat.ID
			must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{root, chat}))
			must(s.ApplyMessages(ctx, []teamsdesktop.Message{
				msg(acctA, root.ID, "root-message", "needle in the team root", base),
				msg(acctA, chat.ID, "chat-message", "needle in an ordinary chat", base),
			}))

			rows, truncated, err := s.Teams(ctx, Filter{Account: &acctA})
			if err != nil || truncated || len(rows) != 1 {
				t.Errorf("team inventory: rows=%+v truncated=%v err=%v", rows, truncated, err)
			} else if row := rows[0]; row.ID != root.ID || row.DisplayName != "ExampleTeam" || row.ChannelCount != 0 || !row.LastActivityAt.Equal(base) {
				t.Errorf("root without cached channels: %+v", row)
			}
			for _, team := range []string{root.ID, "ExampleTeam", "eXAMPLEtEAM"} {
				f := Filter{Account: &acctA, Team: team}
				if err := s.CheckTeam(ctx, &acctA, team); err != nil {
					t.Errorf("resolve %q: %v", team, err)
				}
				messages, _, err := s.Messages(ctx, f)
				if err != nil || !eqStrings(ids(messages), []string{"root-message"}) {
					t.Errorf("messages --team %q: ids=%v err=%v", team, ids(messages), err)
				}
				hits, _, err := s.Search(ctx, "needle", f)
				if err != nil || !eqStrings(ids(hits), []string{"root-message"}) {
					t.Errorf("search --team %q: ids=%v err=%v", team, ids(hits), err)
				}
				conversations, _, err := s.Conversations(ctx, "", "", f)
				if err != nil || len(conversations) != 1 || conversations[0].ID != root.ID {
					t.Errorf("conversations --team %q: rows=%+v err=%v", team, conversations, err)
				}
			}
			for _, team := range []string{chat.ID, chat.DisplayName, "Example", "not-cached"} {
				if err := s.CheckTeam(ctx, &acctA, team); err == nil {
					t.Errorf("%q must not resolve as a team", team)
				}
			}
			if err := s.CheckTeam(ctx, &acctB, root.ID); err == nil {
				t.Error("another account must not resolve this root")
			}
			other, _, err := s.Teams(ctx, Filter{Account: &acctB})
			if err != nil || len(other) != 0 {
				t.Errorf("other account inventory: rows=%+v err=%v", other, err)
			}
		})
	}
}

func TestSpaceOnlyTeamAmbiguityRemainsAccountScoped(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := conv(acctA, "team-a", "Space", "ExampleTeam")
	b := conv(acctB, "team-b", "Space", "exampleteam")
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{a, b}))
	for _, tc := range []struct {
		account teamsdesktop.Account
		id      string
	}{{acctA, a.ID}, {acctB, b.ID}} {
		id, err := s.resolveTeam(ctx, &tc.account, "EXAMPLETEAM")
		if err != nil || id != tc.id {
			t.Errorf("account-scoped resolution: id=%q err=%v", id, err)
		}
	}
	_, err := s.resolveTeam(ctx, nil, "ExampleTeam")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("unscoped duplicate name must be ambiguous: %v", err)
	}
}

func TestTeamChildInferenceRemainsAccountScoped(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	legacy := conv(acctA, "legacy-root", "", "Legacy team")
	child := conv(acctA, "legacy-child", "Topic", "General")
	child.TeamID = legacy.ID
	chat := conv(acctA, "self-chat", "Chat", "Not a team")
	chat.TeamID = chat.ID
	foreignChild := conv(acctB, "foreign-child", "Topic", "Elsewhere")
	foreignChild.TeamID = chat.ID
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{legacy, child, chat, foreignChild}))
	rows, _, err := s.Teams(ctx, Filter{Account: &acctA})
	if err != nil || len(rows) != 1 || rows[0].ID != legacy.ID || rows[0].ChannelCount != 1 {
		t.Errorf("legacy child-reference inference: rows=%+v err=%v", rows, err)
	}
	if err := s.CheckTeam(ctx, &acctA, "Legacy team"); err != nil {
		t.Errorf("legacy team resolution: %v", err)
	}
	if err := s.CheckTeam(ctx, &acctA, chat.ID); err == nil {
		t.Error("self-chat must not borrow another account's child")
	}
}

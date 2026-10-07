package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// seedPolish extends seedReadable so that every list is truncated at Limit 1 and the team,
// total and untitled-chat paths all run: a second channel message, an untitled chat whose members
// are known people, a second activity item and a second person.
func seedPolish(t *testing.T, s *Store) {
	t.Helper()
	seedReadable(t, s)
	ctx := context.Background()
	u1 := conv(acctA, "u1", "Chat", "")
	u1.Members = []string{selfMRI(acctA), "8:orgid:p1", "8:orgid:ghost"}
	u1.ReadHorizonAt = base
	u1.LastMessageAt = base.Add(time.Hour) // newest, so a Limit 1 conversation list is the untitled chat
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{u1}))
	m4 := msg(acctA, "chan", "m4", "second in channel", base.Add(time.Minute))
	m5 := msg(acctA, "u1", "m5", "in the untitled chat", base.Add(2*time.Minute))
	m6 := msg(acctA, "u1", "m6", "again in the untitled chat", base.Add(3*time.Minute))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m4, m5, m6}))
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:p2", DisplayName: "Quinn", SeenAt: base}}))
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a2", Type: "reply", At: base.Add(time.Minute), ConversationID: "u1", MessageID: "m5"}}))
}

// TestPolishReadFailuresSurface is TestReadFailuresSurface for the team filter, the totals, the
// ranked conversation query, filters-only search and the untitled-chat names.
func TestPolishReadFailuresSurface(t *testing.T) {
	acct := &acctA
	var total int
	ops := map[string]func(context.Context, *Store) error{
		"Messages team": func(ctx context.Context, s *Store) error {
			_, _, err := s.Messages(ctx, Filter{Account: acct, Team: "Team", Limit: 1, Total: &total})
			return err
		},
		"Messages untitled": func(ctx context.Context, s *Store) error {
			_, _, err := s.Messages(ctx, Filter{Account: acct, Conversation: "u1", Limit: 1, Total: &total})
			return err
		},
		"Search filters only": func(ctx context.Context, s *Store) error {
			_, _, err := s.Search(ctx, "", Filter{Account: acct, Since: base.Add(-time.Hour), Limit: 1, Total: &total})
			return err
		},
		"Unread": func(ctx context.Context, s *Store) error {
			_, _, err := s.Unread(ctx, Filter{Account: acct, Limit: 1, Total: &total})
			return err
		},
		"UnreadByConversation": func(ctx context.Context, s *Store) error {
			_, _, err := s.UnreadByConversation(ctx, Filter{Account: acct, Limit: 1, Total: &total})
			return err
		},
		"Thread": func(ctx context.Context, s *Store) error {
			_, _, err := s.Thread(ctx, "c1", "m1", Filter{Limit: 1, Total: &total})
			return err
		},
		"Thread channel root": func(ctx context.Context, s *Store) error {
			_, _, err := s.Thread(ctx, "chan", "m3", Filter{Limit: 1})
			return err
		},
		"Thread untitled": func(ctx context.Context, s *Store) error {
			_, _, err := s.Thread(ctx, "u1", "m5", Filter{Limit: 1})
			return err
		},
		"Conversations ranked": func(ctx context.Context, s *Store) error {
			_, _, err := s.Conversations(ctx, "", "Channel", Filter{Account: acct, Team: "Team", Limit: 1, Total: &total})
			return err
		},
		"Conversations untitled": func(ctx context.Context, s *Store) error {
			_, _, err := s.Conversations(ctx, "", "", Filter{Limit: 1, Total: &total})
			return err
		},
		"People": func(ctx context.Context, s *Store) error {
			_, _, err := s.People(ctx, "", Filter{Account: acct, Limit: 1, Total: &total})
			return err
		},
		"Activity": func(ctx context.Context, s *Store) error {
			_, _, err := s.Activity(ctx, ActivityFilter{Account: acct, Type: "mention,reply", Limit: 1, Total: &total})
			return err
		},
		"Activity team": func(ctx context.Context, s *Store) error {
			_, _, err := s.Activity(ctx, ActivityFilter{Account: acct, Team: "Team", Limit: 1})
			return err
		},
		"Activity untitled": func(ctx context.Context, s *Store) error {
			_, _, err := s.Activity(ctx, ActivityFilter{Account: acct, Type: "reply", Limit: 1})
			return err
		},
		"ActivityByKey untitled": func(ctx context.Context, s *Store) error {
			_, err := s.ActivityByKey(ctx, []string{"tenant-1|" + acctA.UserID + "|a2"})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, seedPolish, op) })
	}
}

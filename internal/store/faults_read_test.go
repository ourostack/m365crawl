package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// seedReadable is an archive where every read below returns at least one row: an unread chat
// message, a reply thread, a channel under a team, an activity item and a finished run.
func seedReadable(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	team := conv(acctA, "team", "Space", "Team")
	ch := conv(acctA, "chan", "Topic", "Channel")
	ch.TeamID = "team"
	c1 := conv(acctA, "c1", "Chat", "One")
	c1.ReadHorizonAt = base
	c1.LastMessageAt = base.Add(2 * time.Minute)
	c1.Members = []string{"8:orgid:p1"}
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team, ch, c1}))
	m1 := msg(acctA, "c1", "m1", "hello needle", base.Add(time.Minute))
	m1.Mentions = []teamsdesktop.Mention{{ID: "8:orgid:p1", DisplayName: "Pat"}}
	m1.Reactions = []teamsdesktop.Reaction{{Key: "like", Count: 1}}
	m1.Files = []teamsdesktop.File{{Name: "f.txt"}}
	m1.Links = []string{"https://example.com"}
	m1.Link = "https://teams.example/m1"
	m1.SenderID = "8:orgid:p1"
	m2 := msg(acctA, "c1", "m2", "reply", base.Add(2*time.Minute))
	m2.ParentMessageID, m2.ReplyChainID = "m1", "m1"
	m3 := msg(acctA, "chan", "m3", "in channel", base)
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{m1, m2, m3}))
	must(s.ApplyPeople(ctx, []teamsdesktop.Person{{TenantID: acctA.TenantID, ID: "8:orgid:p1", DisplayName: "Pat", SeenAt: base}, {TenantID: acctA.TenantID, ID: selfMRI(acctA), DisplayName: "Me", SeenAt: base}}))
	must(s.ApplyActivity(ctx, []teamsdesktop.Activity{{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base, ConversationID: "c1", MessageID: "m1"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base.Add(time.Second), Status: "ok", Accounts: []string{"*"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base.Add(time.Second), Source: "s", Fingerprint: "fp", Status: "ok", Counts: map[string]int{"a": 1}, Omissions: map[string]int{"x": 1}}))
}

// TestReadFailuresSurface fails each driver call a read query makes and requires the error to
// reach the caller instead of an empty or partial result.
func TestReadFailuresSurface(t *testing.T) {
	acct := &acctA
	all := Filter{Account: acct, From: "Pat", Conversation: "One", Since: base.Add(-time.Hour), Until: base.Add(time.Hour), MentionsMe: true, IncludeChannels: true}
	byFromID := Filter{Account: acct, From: "8:orgid:p1"}
	mkey := "tenant-1|" + acctA.UserID + "|c1|m1"
	akey := "tenant-1|" + acctA.UserID + "|a1"
	ops := map[string]func(context.Context, *Store) error{
		"Messages": func(ctx context.Context, s *Store) error { _, _, err := s.Messages(ctx, all); return err },
		"Messages by sender id": func(ctx context.Context, s *Store) error {
			_, _, err := s.Messages(ctx, byFromID)
			return err
		},
		"Search": func(ctx context.Context, s *Store) error { _, _, err := s.Search(ctx, "needle", all); return err },
		"Unread": func(ctx context.Context, s *Store) error { _, _, err := s.Unread(ctx, Filter{}); return err },
		"UnreadByConversation": func(ctx context.Context, s *Store) error {
			_, _, err := s.UnreadByConversation(ctx, Filter{})
			return err
		},
		"Thread": func(ctx context.Context, s *Store) error {
			_, _, err := s.Thread(ctx, "c1", "m1", Filter{})
			return err
		},
		"Conversations": func(ctx context.Context, s *Store) error {
			_, _, err := s.Conversations(ctx, "chat", "One", Filter{Account: acct, Since: base, Until: base.Add(time.Hour)})
			return err
		},
		"People": func(ctx context.Context, s *Store) error {
			_, _, err := s.People(ctx, "pat", Filter{Account: acct})
			return err
		},
		"Activity": func(ctx context.Context, s *Store) error {
			_, _, err := s.Activity(ctx, ActivityFilter{Account: acct, Unread: true, Type: "mention", Since: base.Add(-time.Hour)})
			return err
		},
		"Whoami":          func(ctx context.Context, s *Store) error { _, err := s.Whoami(ctx); return err },
		"Status":          func(ctx context.Context, s *Store) error { _, err := s.Status(ctx); return err },
		"LastSuccess":     func(ctx context.Context, s *Store) error { _, err := s.LastSuccess(ctx); return err },
		"LastFingerprint": func(ctx context.Context, s *Store) error { _, err := s.LastFingerprint(ctx, "s"); return err },
		"SQL": func(ctx context.Context, s *Store) error {
			s.readOnly = true // the fault driver replaced the connection; the guard is not under test here
			_, _, _, err := s.SQL(ctx, "select id, 1 from messages", 10)
			return err
		},
		"SQL cut at the limit": func(ctx context.Context, s *Store) error {
			s.readOnly = true
			_, _, _, err := s.SQL(ctx, "select id from messages", 1)
			return err
		},
		"MessageHTML": func(ctx context.Context, s *Store) error {
			_, err := s.MessageHTML(ctx, []MessageKey{{TenantID: acctA.TenantID, UserID: acctA.UserID, ConversationID: "c1", ID: "m1"}})
			return err
		},
		"MessagesByKey": func(ctx context.Context, s *Store) error { _, err := s.MessagesByKey(ctx, []string{mkey}); return err },
		"ActivityByKey": func(ctx context.Context, s *Store) error { _, err := s.ActivityByKey(ctx, []string{akey}); return err },
		"MessagesByKeys": func(ctx context.Context, s *Store) error {
			_, err := s.MessagesByKey(ctx, []string{mkey, "bad"})
			return err
		},
	}
	// Rows whose every Scan destination is a NullString cannot fail on NULL.
	nullSafe := map[string][]int{"Status": {4, 5}, "LastSuccess": {1}, "MessageHTML": {1}, "SQL": {1, 2, 3, 4}, "SQL cut at the limit": {1, 2, 3}}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, seedReadable, op, nullSafe[name]...) })
	}
}

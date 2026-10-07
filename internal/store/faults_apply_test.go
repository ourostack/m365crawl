package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// seedAll fills an archive with one of everything, so a second Apply of changed data takes the
// update paths.
func seedAll(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must(s.ApplyConversations(ctx, faultConvs(false)))
	must(s.ApplyMessages(ctx, faultMsgs(false)))
	must(s.ApplyPeople(ctx, faultPeople(false)))
	must(s.ApplyActivity(ctx, faultActs(false)))
}

// The fault* builders make one batch; changed=true is the same batch edited, so applying it over
// seedAll updates every row.
func faultConvs(changed bool) []teamsdesktop.Conversation {
	team := conv(acctA, "team", "Space", "Team")
	ch := conv(acctA, "chan", "Topic", "Channel")
	ch.TeamID = "team"
	ch.ReadHorizonAt = base
	if changed {
		team.Title = "Team renamed"
		ch.ReadHorizonAt = time.Time{}
		ch.Favorite = true
	}
	return []teamsdesktop.Conversation{team, ch, conv(acctA, "c1", "Chat", "One")}
}

func faultMsgs(changed bool) []teamsdesktop.Message {
	m1, m2 := msg(acctA, "c1", "m1", "hello", base), msg(acctA, "c1", "m2", "world", base.Add(time.Minute))
	if changed {
		m1.Version, m1.ContentText = 2, "hello edited"
		m2.Version, m2.DeletedAt = 2, base.Add(time.Hour)
	}
	return []teamsdesktop.Message{m1, m2}
}

func faultPeople(changed bool) []teamsdesktop.Person {
	p := teamsdesktop.Person{TenantID: acctA.TenantID, ID: "8:orgid:p1", DisplayName: "Pat", SeenAt: base}
	if changed {
		p.DisplayName, p.SeenAt = "Patricia", base.Add(-time.Hour)
	}
	return []teamsdesktop.Person{p}
}

func faultActs(changed bool) []teamsdesktop.Activity {
	a := teamsdesktop.Activity{TenantID: acctA.TenantID, UserID: acctA.UserID, ID: "a1", Type: "mention", At: base, ConversationID: "c1", MessageID: "m1"}
	if changed {
		a.IsRead = true
	}
	return []teamsdesktop.Activity{a}
}

func TestApplyFailuresRollBack(t *testing.T) {
	type apply struct {
		name string
		op   func(ctx context.Context, s *Store, changed bool) error
	}
	applies := []apply{
		{"account", func(ctx context.Context, s *Store, _ bool) error { return s.ApplyAccount(ctx, acctB) }},
		{"conversations", func(ctx context.Context, s *Store, c bool) error {
			_, err := s.ApplyConversations(ctx, faultConvs(c))
			return err
		}},
		{"messages", func(ctx context.Context, s *Store, c bool) error {
			_, _, err := s.ApplyMessagesChanges(ctx, faultMsgs(c))
			return err
		}},
		{"people", func(ctx context.Context, s *Store, c bool) error {
			_, err := s.ApplyPeople(ctx, faultPeople(c))
			return err
		}},
		{"activity", func(ctx context.Context, s *Store, c bool) error {
			_, _, err := s.ApplyActivityChanges(ctx, faultActs(c))
			return err
		}},
		{"run", func(ctx context.Context, s *Store, _ bool) error {
			return s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base, Status: "ok", Counts: map[string]int{"a": 1}, Omissions: map[string]int{"x": 2}})
		}},
	}
	for _, a := range applies {
		for _, mode := range []string{"insert", "update"} {
			t.Run(a.name+"/"+mode, func(t *testing.T) {
				var setup func(*testing.T, *Store)
				if mode == "update" {
					setup = seedAll
				}
				sweepFaults(t, setup, func(ctx context.Context, s *Store) error { return a.op(ctx, s, mode == "update") })
			})
		}
	}
	t.Run("session", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			x, err := s.Begin(ctx)
			if err != nil {
				return err
			}
			defer x.Rollback()
			if err := x.ApplyAccount(ctx, acctA); err != nil {
				return err
			}
			if _, err := x.ApplyConversations(ctx, faultConvs(false)); err != nil {
				return err
			}
			if _, err := x.ApplyMessages(ctx, faultMsgs(false)); err != nil {
				return err
			}
			if _, err := x.ApplyPeople(ctx, faultPeople(false)); err != nil {
				return err
			}
			if _, _, err := x.ApplyActivityChanges(ctx, faultActs(false)); err != nil {
				return err
			}
			if err := x.RecordRun(ctx, Run{StartedAt: base, Status: "ok"}); err != nil {
				return err
			}
			return x.Commit()
		})
	})
}

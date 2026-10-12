package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// seedAppEvidence leaves m1 failed with a retained last good representation, the read that touches
// every evidence query: schema, marker, facts, current and last good.
func seedAppEvidence(t *testing.T, s *Store) {
	t.Helper()
	must(s.ApplyConversations(context.Background(), []teamsdesktop.Conversation{conv(acctA, appConversation, "Meeting", "App chat")}))
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	applyApp(t, s, "m1", 2, appSender, failedRaw)
	applyApp(t, s, "m2", 1, appSender, appRaw("Beta"))
}

func TestMessageAppReadFaults(t *testing.T) {
	reads := map[string]func(ctx context.Context, s *Store) error{
		"messages": func(ctx context.Context, s *Store) error {
			_, _, err := s.Messages(ctx, Filter{Account: &acctA, Limit: 1, Total: new(int)})
			return err
		},
		"thread": func(ctx context.Context, s *Store) error {
			_, _, err := s.Thread(ctx, appConversation, "m1", Filter{Account: &acctA})
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) { sweepFaults(t, seedAppEvidence, read) })
	}
}

func TestMessageAppPublicationFaults(t *testing.T) {
	sweepFaults(t, func(t *testing.T, s *Store) { applyApp(t, s, "m1", 1, appSender, appRaw("Alpha")) }, func(ctx context.Context, s *Store) error {
		_, err := s.ApplyMessages(ctx, []teamsdesktop.Message{appMessage("m1", 2, appSender, appRaw("Beta")), appMessage("m1", 3, appSender, appRaw("Alpha"))})
		return err
	})
}

func TestMessageAppPublicationCancellationRollsBack(t *testing.T) {
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	before := dump(t, s)
	old := projectApplication
	projectApplication = func(context.Context, string, string, []byte, bool) (teamsdesktop.ApplicationProjection, error) {
		return teamsdesktop.ApplicationProjection{}, context.Canceled
	}
	t.Cleanup(func() { projectApplication = old })
	_, err := s.ApplyMessages(context.Background(), []teamsdesktop.Message{appMessage("m1", 2, appSender, appRaw("Beta"))})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became success-shaped: %v", err)
	}
	if dump(t, s) != before {
		t.Fatal("a cancelled projection kept the changed message or its evidence")
	}
}

func TestMessageAppWritableUpgradeAddsDigestColumn(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	for _, q := range []string{`drop table message_app_current`, `drop table message_app_observations`, `alter table messages drop column raw_digest`, `update schema_migrations set version=11`} {
		s.qExec(t, q)
	}
	path := s.cs.Path()
	must0(s.Close())
	up, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = up.Close() })
	env := appEnvelope(t, up, "m1")
	if env.State != AppStateNotDerived || !env.BackfillPending || !env.Current.RawPresent || env.Current.RawDigest != "" {
		t.Fatalf("upgraded archive before backfill: %+v", env)
	}
	applyApp(t, up, "m1", 2, appSender, appRaw("Beta"))
	if env := appEnvelope(t, up, "m1"); env.State != AppStateMapped || servedName(env) != "Beta" {
		t.Fatalf("changed message after upgrade: %+v", env)
	}
}

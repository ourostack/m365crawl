package store

import (
	"context"
	"testing"
	"time"
)

func TestAttendeeLinkedPolicyFaultSweep(t *testing.T) {
	sweepCalendar(t, func(t *testing.T, s *Store) {
		commitOutlook(t, s, time.UTC, "same")
		addTeamsAccount(t, s, "t1/u1")
		putProfile(t, s, "t1", "u1", map[string]string{"mail": "fixture@example.invalid"})
		putIdentity(t, s, "outlook/Main", "fixture@example.invalid")
		if err := s.SetOutlookLink(context.Background(), "outlook/Main", "t1/u1", base); err != nil {
			t.Fatal(err)
		}
	}, func(ctx context.Context, s *Store) error {
		_, err := s.AutoLinkOutlook(ctx, base)
		return err
	})
}

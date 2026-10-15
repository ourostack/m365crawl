package store

import (
	"context"
	"testing"
)

func TestAttendeeCaptureReuseReceipt(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "A", 3)
	a := admitAttendeeFixture(t, s, e, base)
	if reused, err := s.AttendeeCaptureReused(context.Background(), a); err != nil || reused {
		t.Fatal("first snapshot marked reused", err)
	}
	b := admitAttendeeFixture(t, s, attendeeModel(t, "B", 4), base)
	if reused, err := s.AttendeeCaptureReused(context.Background(), b); err != nil || reused {
		t.Fatal("new B marked reused", err)
	}
	a2 := admitAttendeeFixture(t, s, e, base)
	if reused, err := s.AttendeeCaptureReused(context.Background(), a2); err != nil || !reused {
		t.Fatal("A-B-A reuse not reported", err)
	}
	if _, err := s.AttendeeCaptureReused(context.Background(), KnowledgeAttempt{ID: 999, LocatorID: a.LocatorID}); err == nil {
		t.Fatal("unknown capture accepted")
	}
	sweepReadFaults(t, func(t *testing.T, s *Store) { a = admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base) },
		func(ctx context.Context, s *Store) error { _, err := s.AttendeeCaptureReused(ctx, a); return err })
}

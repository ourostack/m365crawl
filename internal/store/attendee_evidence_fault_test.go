package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeReadFaultSweeps(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		addTeamsAccount(t, s, "tenant/A")
		commitOutlook(t, s, time.UTC, "same")
		if err := s.SetOutlookLink(context.Background(), "outlook/Main", "tenant/A", base); err != nil {
			t.Fatal(err)
		}
		admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	}
	for name, op := range map[string]func(context.Context, *Store) error{
		"sources": func(ctx context.Context, s *Store) error { _, err := s.AttendeeSources(ctx, 0); return err },
		"event": func(ctx context.Context, s *Store) error {
			_, err := s.AttendeeEventEvidence(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 100})
			return err
		},
		"history": func(ctx context.Context, s *Store) error {
			_, err := s.AttendeeEventEvidence(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{History: true, Limit: 100})
			return err
		},
		"detail": func(ctx context.Context, s *Store) error { _, err := s.AttendeeDetail(ctx, nil, 1); return err },
		"calendar": func(ctx context.Context, s *Store) error {
			_, err := s.CalendarEvent(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base)))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { sweepFaults(t, seed, op) })
	}
}

func TestAttendeeAdmissionFaultSweep(t *testing.T) {
	var attempt KnowledgeAttempt
	var readID int64
	setup := func(t *testing.T, s *Store) {
		admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
		result, err := s.CommitOutlook(context.Background(), OutlookBatch{Account: "outlook/Main", Origin: attendeeFixtureOrigin, At: base, Zone: time.UTC, Stamp: "same", Events: []calendar.Event{outlookEvent(calendar.TriFalse, base)}},
			func(CalendarResult) Run {
				return Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "synthetic", Status: "ok"}
			})
		if err != nil {
			t.Fatal(err)
		}

		readID = result.OutlookReadID
		attempt, err = s.BeginAttendeeAttempt(context.Background(), KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: attendeeFixtureOrigin, AcquisitionSignature: "synthetic", At: base}, AttendeeRequest{Root: attendeeFixtureRoot, Discover: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		return s.CommitAttendeeCapture(ctx, attempt, AttendeeCapture{Evidence: attendeeModel(t, "B", 4), InputDigest: strings.Repeat("a", 64), CalendarReadID: readID, AcquiredAt: base, AdmittedAt: base})
	})
}

func TestAttendeeFaultDumpIncludesNativeState(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	before := dump(t, s)
	s.qExec(t, `update outlook_attendee_members set membership_digest='synthetic_corruption'`)
	if after := dump(t, s); after == before {
		t.Fatal("transaction fault oracle omitted immutable native state")
	}
}

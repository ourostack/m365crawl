package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAttendeeBootstrapUsesAdmittedRequestHistory(t *testing.T) {
	ctx := context.Background()
	for _, hasRoot := range []bool{false, true} {
		name := "no-root-in-history"
		if hasRoot {
			name = "original-admitted-root"
		}
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			model := attendeeModel(t, "Synthetic", 3)
			initial := AttendeeRequest{SourceID: 1}
			if hasRoot {
				initial = AttendeeRequest{Root: "/synthetic/native", Discover: true}
			}
			cal, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Origin: attendeeFixtureOrigin,
				At: base, Zone: time.UTC, Stamp: "same"}, func(CalendarResult) Run {
				return Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "synthetic", Status: "ok"}
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, request := range []AttendeeRequest{initial, {SourceID: 1}} {
				a, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture",
					Locator: attendeeFixtureOrigin, AcquisitionSignature: "synthetic", At: base}, request)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.CommitAttendeeCapture(ctx, a, AttendeeCapture{Evidence: model, InputDigest: strings.Repeat("a", 64),
					CalendarReadID: cal.OutlookReadID, AcquiredAt: base, AdmittedAt: base}); err != nil {
					t.Fatal(err)
				}
			}
			failed, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture",
				Locator: attendeeFixtureOrigin, At: base}, AttendeeRequest{Root: "/synthetic/unadmitted", Discover: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAttendeeFailure(ctx, failed, KnowledgeOutcome{Status: "failed", At: base}); err != nil {
				t.Fatal(err)
			}
			request, err := s.AttendeeBootstrapRequest(ctx, 1)
			want := ""
			if hasRoot {
				want = "/synthetic/native"
			}
			if err != nil || request.Root != want || request.SourceID != 0 || request.Discover != hasRoot {
				t.Fatal("bootstrap guessed, used a failed request or lost original intent", request, err)
			}
			for _, id := range []int64{-1, 0, 999} {
				if _, err := s.AttendeeBootstrapRequest(ctx, id); !errors.Is(err, ErrKnowledgeSourceUnknown) {
					t.Fatal("unknown source yielded recovery authority", id, err)
				}
			}
			s.qExec(t, `update schema_migrations set version=18`)
			if _, err := s.AttendeeBootstrapRequest(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
				t.Fatal("old namespace yielded bootstrap evidence", err)
			}
		})
	}
}

func TestAttendeeBootstrapRejectsCorruptHistoricalRequests(t *testing.T) {
	ctx := context.Background()
	for _, query := range []string{
		`update outlook_attendee_requests set request_json='{}'`,
		`drop trigger outlook_attendee_event_insert`,
		`delete from outlook_link_state`,
	} {
		t.Run(query, func(t *testing.T) {
			s := newStore(t)
			admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			s.qExec(t, query)
			if _, err := s.AttendeeBootstrapRequest(ctx, 1); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatal("bootstrap bypassed namespace/request/membership audit", err)
			}
		})
	}
}

func TestAttendeeBootstrapRequestReadFaults(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	}, func(ctx context.Context, s *Store) error {
		_, err := s.AttendeeBootstrapRequest(ctx, 1)
		return err
	})
}

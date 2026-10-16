package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeUnadmittedRegistrationCoverage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Origin: attendeeFixtureOrigin, At: base, Zone: time.UTC, Events: []calendar.Event{outlookEvent(calendar.TriFalse, base)}}, outlookRun)
	if err != nil {
		t.Fatal(err)
	}
	a := nativeAttempt(t, s)
	if err := s.RecordAttendeeFailure(ctx, a, KnowledgeOutcome{Status: "failed", Reason: "synthetic_failure", At: base}); err != nil {
		t.Fatal(err)
	}
	got := attendeeEventFixture(t, s, false)
	if len(got.Coverage.Sources) != 1 || got.Coverage.Sources[0].Status != "failed" || got.Coverage.Sources[0].SourceID != nil || len(got.Rows) != 0 {
		t.Fatal("registered origin's failed native attempt disappeared from event coverage")
	}
}

func TestAttendeeDetailOtherParentAndInvalidUnadmittedClock(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "A", 3)
	b := e.Selections[0]
	b.DetailKey = 4
	b.SourceID = "BB22"
	e.Selections = append(e.Selections, b)
	admitAttendeeFixture(t, s, e, base)
	detail, err := s.AttendeeDetail(context.Background(), nil, 1)
	if err != nil || detail.BindingsTotal != 1 {
		t.Fatal("detail borrowed another parent's binding", err)
	}
	other := newStore(t)
	nativeAttempt(t, other)
	other.qExec(t, `update knowledge_attempts set started_at='invalid'`)
	ctx := context.Background()
	err = other.inTx(ctx, func(tx *sql.Tx) error { _, err := attendeeSourcesInView(ctx, tx, nil, 0); return err })
	if !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("invalid pending attempt clock accepted", err)
	}
}

func TestAttendeeUnadmittedScopeFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		ctx := context.Background()
		for _, profile := range []string{"Main", "Other"} {
			e := outlookEvent(calendar.TriFalse, base)
			e.AccountID = "outlook/" + profile
			if profile == "Other" {
				e.SourceID, e.GlobalID, e.ICalUID, e.SeriesKey = "BB22", "BB22", "BB22", "BB22"
			}
			origin := filepath.Join(attendeeFixtureRoot, profile, "HxStore.hxd")
			if _, err := s.CommitOutlook(ctx, OutlookBatch{Account: e.AccountID, Origin: origin, At: base, Zone: time.UTC, Events: []calendar.Event{e}}, outlookRun); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: origin, At: base}, AttendeeRequest{Root: attendeeFixtureRoot, Discover: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.AttendeeEventEvidence(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 100})
		return err
	})
}

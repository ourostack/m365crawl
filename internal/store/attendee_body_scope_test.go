package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeRelevantBodyScope(t *testing.T) {
	s := newStore(t)
	old := attendeeModel(t, "Old unrelated", 3)
	old.Selections[0].SourceID = "BB22"
	oldEvent := outlookEvent(calendar.TriFalse, base)
	oldEvent.SourceID, oldEvent.GlobalID, oldEvent.ICalUID, oldEvent.SeriesKey = "BB22", "BB22", "BB22", "BB22"
	admitAttendeeFixture(t, s, old, base, oldEvent)
	admitAttendeeFixture(t, s, attendeeModel(t, "Current", 4), base)
	s.qExec(t, `update outlook_attendee_snapshots set result_json='{}' where rowid=1`)
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 1 {
		t.Fatal("default read certified or depended on an unrelated historical body")
	}
	if got := attendeeEventFixture(t, s, true); len(got.Rows) != 1 {
		t.Fatal("history audited a snapshot not associated with this event")
	}
	if _, err := s.AttendeeDetail(context.Background(), nil, 1); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("own historical detail ignored corrupt body")
	}
	if _, err := s.AttendeeEventEvidence(context.Background(), nil, calendar.Key(oldEvent), AttendeeFilter{History: true, Limit: 100}); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("associated history ignored corrupt body")
	}
}

func TestAttendeeHistoricalIndexCannotHideEligibility(t *testing.T) {
	s := newStore(t)
	old := attendeeModel(t, "Old unrelated", 3)
	old.Selections[0].SourceID = "BB22"
	oldEvent := outlookEvent(calendar.TriFalse, base)
	oldEvent.SourceID, oldEvent.GlobalID, oldEvent.ICalUID, oldEvent.SeriesKey = "BB22", "BB22", "BB22", "BB22"
	admitAttendeeFixture(t, s, old, base, oldEvent)
	admitAttendeeFixture(t, s, attendeeModel(t, "Current", 4), base)
	s.qExec(t, `update outlook_attendee_bindings set native_event_id='ZZ99' where snapshot_id=1`)
	if _, err := s.AttendeeEventEvidence(context.Background(), nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 1}); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("mutable index changed intent eligibility without detection")
	}
}

type attendeeQueryObserver struct {
	knowledgeReader
	bodies *int
}

func (q attendeeQueryObserver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "select result_json") {
		*q.bodies++
	}
	return q.knowledgeReader.QueryRowContext(ctx, query, args...)
}

func TestAttendeeSQLByteGuard(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "💫", 3), base)
	ctx := context.Background()
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		var size int
		if err := tx.QueryRowContext(ctx, `select length(cast(result_json as blob)) from outlook_attendee_snapshots where rowid=1`).Scan(&size); err != nil {
			return err
		}
		bodies := 0
		reader := attendeeQueryObserver{tx, &bodies}
		if _, err := attendeeBlob(ctx, reader, "outlook_attendee_snapshots", "result_json", "rowid", 1, size-1); !errors.Is(err, ErrAttendeeArchiveCorrupt) || bodies != 0 {
			t.Fatal("guard materialized UTF8 body before checking SQL byte length")
		}
		if _, err := attendeeBlob(ctx, reader, "outlook_attendee_snapshots", "result_json", "rowid", 1, size); err != nil || bodies != 1 {
			t.Fatal("exact real SQL byte boundary refused")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttendeeNewAdmissionCannotHideCorruptCurrentBody(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	s.qExec(t, `update outlook_attendee_snapshots set result_json='{}'`)
	ctx := context.Background()
	cal, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Origin: attendeeFixtureOrigin, At: base, Zone: time.UTC, Events: []calendar.Event{outlookEvent(calendar.TriFalse, base)}},
		func(CalendarResult) Run {
			return Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Status: "ok", Fingerprint: "synthetic"}
		})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: attendeeFixtureOrigin, AcquisitionSignature: "synthetic", At: base}, AttendeeRequest{Root: "/synthetic/native", Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	err = s.CommitAttendeeCapture(ctx, a, AttendeeCapture{Evidence: attendeeModel(t, "B", 4), InputDigest: strings.Repeat("a", 64), CalendarReadID: cal.OutlookReadID, AcquiredAt: base, AdmittedAt: base})
	if !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("new representation hid corrupt old current body")
	}
}

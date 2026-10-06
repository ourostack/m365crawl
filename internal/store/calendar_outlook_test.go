package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/outlookcal"
)

func outlookEvent(allDay calendar.Tri, lm time.Time) calendar.Event {
	return calendar.Event{
		Source: calendar.SourceOutlook, AccountID: "outlook/Main", SourceID: "AA11", GlobalID: "AA11", ICalUID: "AA11", SeriesKey: "AA11",
		EventType: calendar.EventSingle, Start: time.Date(2031, 3, 5, 9, 30, 0, 0, time.UTC), End: time.Date(2031, 3, 5, 10, 0, 0, 0, time.UTC),
		LastModified: &lm, Subject: "Fixture", AllDay: allDay, UnknownDeclared: true,
		Unknown: []calendar.Field{calendar.FieldUTCOffset, calendar.FieldShortJoinURL, calendar.FieldRooms, calendar.FieldAttachments,
			calendar.FieldCategories, calendar.FieldRecurrence, calendar.FieldReminder, calendar.FieldJoinURL, calendar.FieldDialIn,
			calendar.FieldMeetingChatID, calendar.FieldBody, calendar.FieldAttendees, calendar.FieldTimeZone, calendar.FieldTimeZoneIANA,
			calendar.FieldShowAs, calendar.FieldResponse},
	}
}

func outlookRun(CalendarResult) Run {
	return Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Status: "ok"}
}

func commitOutlook(t *testing.T, s *Store, zone *time.Location, stamp string, events ...calendar.Event) CalendarResult {
	t.Helper()
	res, err := s.CommitOutlook(context.Background(), OutlookBatch{Account: "outlook/Main", Events: events, FreshAt: base, At: base, Zone: zone, Stamp: stamp}, outlookRun)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func applyOutlookBatch(t *testing.T, s *Store, stamp string, events ...calendar.Event) CalendarResult {
	t.Helper()
	return commitOutlook(t, s, time.UTC, stamp, events...)
}

func allDayColumn(t *testing.T, s *Store) (v *int) {
	t.Helper()
	if err := s.db.QueryRow(`select all_day from calendar_source_events where source='outlook' and source_id='AA11'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A mapper that stored an all-day flag as false is replaced by one that says unknown only when the
// stamp changed: Capture keeps a stored flag over an unknown one, so the stamp makes the batch
// blank its events first.
func TestOutlookMapperBumpMakesStoredAllDayUnknown(t *testing.T) {
	s := newStore(t)
	v1, v3 := OutlookStamp(1, time.UTC), OutlookStamp(outlookcal.MapperVersion, time.UTC)
	lm := base
	applyOutlookBatch(t, s, v1, outlookEvent(calendar.TriFalse, lm))
	if v := allDayColumn(t, s); v == nil || *v != 0 {
		t.Fatalf("stored all-day %v, want false", v)
	}
	// The same stamp: the unknown flag does not replace the stored false.
	applyOutlookBatch(t, s, v1, outlookEvent(calendar.TriUnknown, lm))
	if v := allDayColumn(t, s); v == nil || *v != 0 {
		t.Fatalf("an unknown flag replaced the stored one: %v", v)
	}
	// A new stamp re-derives the event: the stored false becomes unknown, and the row keeps its key.
	res := applyOutlookBatch(t, s, v3, outlookEvent(calendar.TriUnknown, lm))
	if v := allDayColumn(t, s); v != nil {
		t.Fatalf("stored all-day %v, want unknown", *v)
	}
	if res.Counts.Events.Seen != 1 || res.Counts.Events.Inserted != 0 {
		t.Fatalf("%+v", res.Counts)
	}
	var rows, covered int
	_ = s.db.QueryRow(`select count(*) from calendar_source_events where source='outlook'`).Scan(&rows)
	_ = s.db.QueryRow(`select count(*) from calendar_covered_days where source='outlook'`).Scan(&covered)
	if rows != 1 || covered != 1 {
		t.Fatalf("%d rows, %d covered days", rows, covered)
	}
}

func TestOutlookZoneChangeTakesCoveredDaysAgain(t *testing.T) {
	s := newStore(t)
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	east := time.FixedZone("east", 15*3600) // 09:30 UTC is already the next day
	commitOutlook(t, s, east, OutlookStamp(3, east), outlookEvent(calendar.TriFalse, base))
	var day string
	var n int
	if err := s.db.QueryRow(`select min(day), count(*) from calendar_covered_days where source='outlook'`).Scan(&day, &n); err != nil || day != "2031-03-06" || n != 1 {
		t.Fatalf("%q %d %v", day, n, err)
	}
}

func TestOutlookState(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	st, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || !st.LastAttempt.IsZero() || st.Fingerprint != "" || st.Omissions != nil || st.HoldsEvents {
		t.Fatalf("%+v %v", st, err)
	}
	if err := s.SetOutlookLastAttempt(ctx, "Main", base); err != nil {
		t.Fatal(err)
	}
	applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
	if err := s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "fp", Status: "ok_with_omissions", Omissions: map[string]int{"calendar_unmapped": 2}}); err != nil {
		t.Fatal(err)
	}
	st, err = s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || !st.LastAttempt.Equal(base) || st.Fingerprint != "fp" || st.Omissions["calendar_unmapped"] != 2 || !st.HoldsEvents {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := s.db.Exec(`update sync_runs set omissions_json='{' where source='outlook|Main'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); err == nil {
		t.Fatal("a damaged omissions row is an error")
	}
}

// Every database call of a commit and of a state read can fail, and a failed commit leaves nothing.
func TestOutlookFailuresRollBack(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		applyOutlookBatch(t, s, OutlookStamp(1, time.UTC), outlookEvent(calendar.TriFalse, base))
	}
	east := time.FixedZone("east", 15*3600)
	master := outlookEvent(calendar.TriFalse, base)
	master.SourceID, master.GlobalID, master.ICalUID, master.SeriesKey, master.EventType = "BB22", "BB22", "BB22", "BB22", calendar.EventMaster
	allDay := outlookEvent(calendar.TriTrue, base)
	allDay.SourceID, allDay.GlobalID, allDay.ICalUID, allDay.SeriesKey = "CC33", "CC33", "CC33", "CC33"
	allDay.Start, allDay.End, allDay.StartDate, allDay.EndDate = time.Date(2031, 3, 6, 0, 0, 0, 0, time.UTC), time.Date(2031, 3, 7, 0, 0, 0, 0, time.UTC), "2031-03-06", "2031-03-07"
	t.Run("commit", func(t *testing.T) {
		// A new stamp and a new zone: the stamp read, the covered-day delete, the blank, the batch and the run row.
		sweepCalendar(t, seed, func(ctx context.Context, s *Store) error {
			_, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriUnknown, base), master, allDay}, FreshAt: base, At: base, Zone: east, Stamp: OutlookStamp(3, east)}, outlookRun)
			return err
		})
	})
	t.Run("state", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if err := s.RecordRun(context.Background(), Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Fingerprint: "fp", Status: "ok_with_omissions", Omissions: map[string]int{"calendar_unmapped": 2}}); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			_, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
			return err
		}, 3)
	})
}

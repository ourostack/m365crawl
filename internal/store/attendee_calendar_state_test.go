package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeCalendarSelectionStates(t *testing.T) {
	for _, tc := range []struct{ name, sql, state string }{
		{"missing", `update outlook_attendee_bindings set native_event_id='ZZ99'`, "row_missing"},
		{"removed", `update calendar_source_events set removed_at='2026-01-01T00:00:00.000Z' where source='outlook'`, "removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			if tc.name == "missing" {
				// A valid capture may contain a selected parent not present in the resolved group.
				e := attendeeModel(t, "B", 4)
				e.Selections[0].SourceID = "ZZ99"
				admitAttendeeFixture(t, s, e, base)
			} else {
				s.qExec(t, tc.sql)
			}
			got, err := s.AttendeeEventEvidence(context.Background(), nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 0 || len(got.Coverage.Sources) != 1 || got.Coverage.Sources[0].CalendarState != tc.state {
				t.Fatalf("wrong selection state: %#v", got)
			}
		})
	}
}

func TestAttendeeMultipleSelectionPreservesReadMismatch(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "A", 3)
	second := e.Selections[0]
	second.BlockOffset++
	e.Selections = append(e.Selections, second)
	e.Parents[0].EventLinks = 2
	admitAttendeeFixture(t, s, e, base)
	if got := attendeeEventFixture(t, s, false); got.Coverage.Sources[0].CalendarState != "multiple_selections" {
		t.Fatal("distinct receipts collapsed")
	}
	commitOutlook(t, s, time.UTC, "same")
	got := attendeeEventFixture(t, s, false)
	if len(got.Rows) != 0 || got.Coverage.Sources[0].CalendarState == "multiple_selections" {
		t.Fatal("selection multiplicity hid stronger read mismatch")
	}
}

package store

import (
	"context"
	"slices"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeCalendarEventCoherentSummary(t *testing.T) {
	s := newStore(t)
	e := outlookEvent(calendar.TriFalse, base)
	e.Unknown = slices.DeleteFunc(e.Unknown, func(f calendar.Field) bool { return f == calendar.FieldAttendees })
	e.AttendeesJSON = `[{"name":"Legacy A","address":"a@example.invalid"},{"name":"Legacy B","address":"b@example.invalid"}]`
	admitAttendeeFixture(t, s, attendeeModel(t, "Native", 3), base, e)
	got, err := s.CalendarEvent(context.Background(), nil, calendar.Key(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attendees) != 2 || got.NativeAttendeeEvidence.Total != 1 || len(got.NativeAttendeeEvidence.Rows) != 0 ||
		got.NativeAttendeeEvidence.Coverage.Scope != AttendeeScope || len(got.NativeAttendeeEvidence.Coverage.Sources) != 1 {
		t.Fatal("native body-free summary replaced legacy roster or lacked coherent source evidence")
	}
}

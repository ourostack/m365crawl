package outlookowacal

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/v8"
)

func object(values ...any) *v8.Object {
	if len(values)%2 != 0 {
		panic("synthetic object requires key/value pairs")
	}
	o := &v8.Object{}
	for i := 0; i+1 < len(values); i += 2 {
		o.Keys = append(o.Keys, values[i].(string))
		o.Values = append(o.Values, values[i+1])
	}
	return o
}

func event() *v8.Object {
	return object(
		"UID", "AABBCCDDEEFF0011223344556677889900000000AABB",
		"Start", "2026-10-20T10:00:00-07:00",
		"End", "2026-10-20T11:00:00-07:00",
		"LastModifiedTime", "2026-10-19T12:00:00Z",
		"Subject", "Synthetic calendar event",
		"Location", "Synthetic room",
		"Organizer", object("Mailbox", object("Name", "Synthetic organizer", "EmailAddress", "organizer@example.test")),
		"StartTimeZoneId", "Pacific Standard Time",
		"CalendarItemType", "Single",
		"ResponseType", "Accept",
		"IsAllDayEvent", false,
		"IsCancelled", false,
		"OnlineMeetingJoinUrl", "https://teams.microsoft.com/l/meetup-join/synthetic",
	)
}

func replace(o *v8.Object, key string, value any) {
	for i, k := range o.Keys {
		if k == key {
			o.Values[i] = value
			return
		}
	}
	o.Keys, o.Values = append(o.Keys, key), append(o.Values, value)
}

func TestMapEventUsesCalendarCoreContract(t *testing.T) {
	e, notes, err := MapEvent("outlook/Synthetic", event())
	if err != nil {
		t.Fatal(err)
	}
	if e.Source != calendar.SourceOutlook || e.AccountID != "outlook/Synthetic" ||
		e.SourceID != "aabbccddeeff0011223344556677889900000000aabb" ||
		e.GlobalID != e.SourceID || e.ICalUID != e.SourceID || e.SeriesKey != e.SourceID {
		t.Fatal("calendar identity was changed")
	}
	if e.Start.Format(time.RFC3339) != "2026-10-20T17:00:00Z" || e.End.Format(time.RFC3339) != "2026-10-20T18:00:00Z" ||
		e.LastModified == nil || e.LastModified.Format(time.RFC3339) != "2026-10-19T12:00:00Z" {
		t.Fatal("source clocks were changed")
	}
	if e.Subject != "Synthetic calendar event" || e.Location != "Synthetic room" || e.Organizer != "Synthetic organizer" ||
		e.OrganizerAddress != "organizer@example.test" || e.Response != "accepted" || e.EventType != calendar.EventSingle {
		t.Fatal("stated fields were lost")
	}
	if e.TimeZone != "Pacific Standard Time" || e.TimeZoneIANA != "America/Los_Angeles" || !e.Cancelled.Is(false) || !e.AllDay.Is(false) {
		t.Fatal("zone or stated flags were changed")
	}
	if e.IsOnlineMeeting.Known() || e.IsOrganizer.Known() || e.IsPrivate.Known() || e.HasAttachments.Known() {
		t.Fatal("an unstated flag was invented")
	}
	if e.OnlineMeetingURL != "https://teams.microsoft.com/l/meetup-join/synthetic" || notes != (Notes{}) {
		t.Fatal("join link or mapping notes differ")
	}
	calendar.AssertEveryFieldClassified(t, e)
	if err := calendar.ValidateEvent(e); err != nil {
		t.Fatal(err)
	}
}

func TestMapEventMissingFieldsStayUnknown(t *testing.T) {
	o := object("UID", "AABB", "Start", "2026-10-20T10:00:00Z", "End", "2026-10-20T11:00:00Z", "LastModifiedTime", "2026-10-19T12:00:00Z")
	e, notes, err := MapEvent("outlook/Synthetic", o)
	if err != nil || notes != (Notes{}) {
		t.Fatalf("sparse event refused: %v", err)
	}
	if e.AllDay.Known() || e.Cancelled.Known() || e.EventType != "" || e.Response != "" || e.OriginalStart != nil ||
		e.BodyHTML != "" || e.AttendeesJSON != "" || e.RecurrenceJSON != "" {
		t.Fatal("sparse fields were fabricated")
	}
	calendar.AssertEveryFieldClassified(t, e)
	replace(o, "Subject", "")
	replace(o, "Location", v8.Undefined{})
	replace(o, "Organizer", nil)
	e, notes, err = MapEvent("outlook/Synthetic", o)
	if err != nil || notes != (Notes{}) {
		t.Fatal("absent optional values were counted as malformed")
	}
	calendar.AssertEveryFieldClassified(t, e, calendar.FieldSubject)
}

func TestMapEventRefusesRequiredDamageWithoutValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"UID", "private-not-hex"}, {"UID", "ABC"}, {"UID", ""}, {"UID", nil},
		{"Start", "private-date"}, {"Start", nil}, {"End", "2026-10-20T09:00:00-07:00"},
		{"LastModifiedTime", 123.0}, {"LastModifiedTime", "0001-01-01T00:00:00Z"},
	} {
		o := event()
		replace(o, tc.name, tc.value)
		got, _, err := MapEvent("outlook/Synthetic", o)
		if err == nil || !reflect.DeepEqual(got, calendar.Event{}) {
			t.Fatalf("%s admitted damaged event", tc.name)
		}
		if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "Synthetic") {
			t.Fatal("error exposed a source value")
		}
	}
	for _, value := range []any{nil, "private-root", (*v8.Object)(nil), &v8.Object{Keys: []string{"UID"}}} {
		if _, _, err := MapEvent("outlook/Synthetic", value); err == nil {
			t.Fatal("malformed event admitted")
		}
	}
	if _, _, err := MapEvent("", event()); err == nil {
		t.Fatal("empty account admitted")
	}
	o := event()
	o.Keys, o.Values = append(o.Keys, "UID"), append(o.Values, "private-duplicate")
	if _, _, err := MapEvent("outlook/Synthetic", o); err == nil {
		t.Fatal("duplicate identity admitted")
	}
}

func TestMapEventOptionalDamageIsCountedAndUnknown(t *testing.T) {
	for _, field := range []string{"Subject", "Location", "Organizer", "StartTimeZoneId", "ResponseType", "CalendarItemType", "IsCancelled", "IsAllDayEvent", "OnlineMeetingJoinUrl"} {
		o := event()
		replace(o, field, 123.0)
		e, notes, err := MapEvent("outlook/Synthetic", o)
		if err != nil || notes.Unmapped != 1 {
			t.Fatalf("%s: notes=%+v err=%v", field, notes, err)
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
	for _, value := range []any{"private-future-response", "", "\xff"} {
		o := event()
		replace(o, "ResponseType", value)
		e, notes, err := MapEvent("outlook/Synthetic", o)
		if err != nil || notes.Unmapped != 1 || e.Response != "" {
			t.Fatal("unrecognized response was guessed")
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
	o := event()
	replace(o, "StartTimeZoneId", "Unrecognized Synthetic Zone")
	e, notes, err := MapEvent("outlook/Synthetic", o)
	if err != nil || notes.UnknownZone != 1 || e.TimeZone != "Unrecognized Synthetic Zone" || e.TimeZoneIANA != "" {
		t.Fatal("unknown zone was resolved without evidence")
	}
	calendar.AssertEveryFieldClassified(t, e)
}

func TestMapEventPreservesOccurrenceAndAllDayUncertainty(t *testing.T) {
	o := event()
	replace(o, "UID", "AABBCCDDEEFF0011223344556677889907EA0A14AABB")
	replace(o, "CalendarItemType", "Exception")
	replace(o, "IsCancelled", true)
	e, _, err := MapEvent("outlook/Synthetic", o)
	if err != nil || e.SeriesKey != "aabbccddeeff0011223344556677889900000000aabb" || e.EventType != calendar.EventException ||
		!e.Cancelled.Is(true) || e.OriginalStart != nil {
		t.Fatal("occurrence identity or explicit cancellation was changed")
	}
	replace(o, "IsAllDayEvent", true)
	e, notes, err := MapEvent("outlook/Synthetic", o)
	if err != nil || e.AllDay.Known() || notes.AllDayUnaligned != 1 || e.StartDate != "" {
		t.Fatal("unaligned all-day dates were invented")
	}
	replace(o, "Start", "2026-10-20T00:00:00Z")
	replace(o, "End", "2026-10-22T00:00:00Z")
	e, notes, err = MapEvent("outlook/Synthetic", o)
	if err != nil || !e.AllDay.Is(true) || e.StartDate != "2026-10-20" || e.EndDate != "2026-10-22" || notes != (Notes{}) {
		t.Fatal("aligned explicit all-day span was lost")
	}
	calendar.AssertEveryFieldClassified(t, e)
	for _, tc := range []struct{ input, want string }{
		{"Occurrence", calendar.EventOccurrence}, {"RecurringMaster", calendar.EventMaster}, {"Single", calendar.EventSingle}, {"private-new-type", ""},
	} {
		replace(o, "CalendarItemType", tc.input)
		e, notes, err = MapEvent("outlook/Synthetic", o)
		if err != nil || e.EventType != tc.want || (tc.want == "" && notes.Unmapped != 1) {
			t.Fatal("event type mapping differs")
		}
	}
}

func TestMapEventResponseAndScrub(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Accept", "accepted"}, {"Tentative", "tentative"}, {"Decline", "declined"},
		{"NoResponseReceived", "none"}, {"Organizer", "organizer"},
	} {
		o := event()
		replace(o, "ResponseType", tc.input)
		e, notes, err := MapEvent("outlook/Synthetic", o)
		if err != nil || e.Response != tc.want || notes != (Notes{}) {
			t.Fatal("response mapping differs")
		}
	}
	o := event()
	replace(o, "OnlineMeetingJoinUrl", "https://example.test/join?sig=synthetic-secret")
	e, notes, err := MapEvent("outlook/Synthetic", o)
	if err != nil || notes.Redacted == 0 || strings.Contains(e.OnlineMeetingURL, "synthetic-secret") {
		t.Fatal("credential-shaped link was not scrubbed")
	}
	if !reflect.DeepEqual(e.Unknown, calendar.NormalizeUnknown(e.Unknown)) {
		t.Fatal("unknown vocabulary is not canonical")
	}
}

func TestMapEventMeetingThreadAndMalformedMailbox(t *testing.T) {
	o := event()
	replace(o, "OnlineMeetingJoinUrl", "https://teams.microsoft.com/l/meetup-join/19%3ameeting_Synthetic%40thread.v2/0")
	e, notes, err := MapEvent("outlook/Synthetic", o)
	if err != nil || notes != (Notes{}) || e.TeamsThreadID != "19:meeting_Synthetic@thread.v2" {
		t.Fatal("explicit Teams meeting link did not supply its thread")
	}
	calendar.AssertEveryFieldClassified(t, e)
	for _, mailbox := range []any{"private-malformed", (*v8.Object)(nil), object("Name", "Synthetic", "Name", "conflict")} {
		replace(o, "Organizer", object("Mailbox", mailbox))
		e, notes, err = MapEvent("outlook/Synthetic", o)
		if err != nil || notes.Unmapped != 1 || e.Organizer != "" || e.OrganizerAddress != "" {
			t.Fatal("malformed mailbox was accepted as a stated organizer")
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
}

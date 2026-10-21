package outlookowacal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func unknown(e calendar.Event, field calendar.Field) bool {
	for _, f := range e.Unknown {
		if f == field {
			return true
		}
	}
	return false
}

func attendee(name, address, response string) any {
	return object("Mailbox", object("Name", name, "EmailAddress", address), "ResponseType", response)
}

func TestMapEventCachedHTMLAndTextBody(t *testing.T) {
	for _, tc := range []struct{ kind, value, html, text string }{
		{"HTML", "<p>Synthetic &amp; cached</p>", "<p>Synthetic &amp; cached</p>", "Synthetic & cached"},
		{"html", "", "", ""},
		{"Text", "<b>Synthetic literal text</b>", "", "<b>Synthetic literal text</b>"},
	} {
		o := event()
		replace(o, "Body", object("BodyType", tc.kind, "Value", tc.value))
		replace(o, "Preview", "Synthetic preview")
		e, notes, err := MapEvent("A", o)
		if err != nil || notes != (Notes{}) || e.BodyHTML != tc.html || e.BodyText != tc.text || e.BodyType != strings.ToLower(tc.kind) ||
			e.BodyPreview != "Synthetic preview" || unknown(e, calendar.FieldBody) || unknown(e, calendar.FieldBodyPreview) {
			t.Fatalf("cached body mapping: notes=%+v err=%v", notes, err)
		}
		if tc.value == "" {
			calendar.AssertEveryFieldClassified(t, e, calendar.FieldBody)
		} else {
			calendar.AssertEveryFieldClassified(t, e)
		}
	}
	o := event()
	replace(o, "Body", object("BodyType", "HTML", "Value", `<a href="https://example.test?sig=synthetic-secret">Synthetic</a>`))
	e, notes, err := MapEvent("A", o)
	if err != nil || notes.Redacted == 0 || strings.Contains(e.BodyHTML, "synthetic-secret") || e.BodyText != "Synthetic" {
		t.Fatal("cached body credential-shaped values were not scrubbed")
	}
}

func TestMapEventMissingOrDamagedBodyNeverClearsKnownDetail(t *testing.T) {
	for _, tc := range []struct {
		value any
		loss  int
	}{
		{nil, 0}, {object(), 0}, {"private-body", 1},
		{object("BodyType", "HTML"), 1},
		{object("Value", "private-body"), 1},
		{object("BodyType", "Unsupported", "Value", "private-body"), 1},
		{object("BodyType", "HTML", "Value", 42.0), 1},
		{object("BodyType", 42.0, "Value", "private-body"), 1},
		{object("BodyType", "HTML", "Value", "private-body", "IsTruncated", true), 1},
		{object("BodyType", "HTML", "Value", "private-body", "IsTruncated", "private-invalid"), 1},
		{object("BodyType", "HTML", "Value", "private-body", "Value", "conflict"), 1},
	} {
		o := event()
		replace(o, "Body", tc.value)
		e, notes, err := MapEvent("A", o)
		if err != nil || notes.Unmapped != tc.loss || !unknown(e, calendar.FieldBody) || e.BodyHTML != "" || e.BodyText != "" || e.BodyType != "" {
			t.Fatalf("damaged body admitted: notes=%+v err=%v", notes, err)
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
}

func TestMapEventAllAttendeeRolesPreserveOrderAndCoreJSON(t *testing.T) {
	o := event()
	replace(o, "RequiredAttendees", []any{attendee("Synthetic required", "required@example.test", "Accept")})
	replace(o, "OptionalAttendees", []any{attendee("Synthetic optional", "optional@example.test", "Tentative")})
	replace(o, "Resources", []any{attendee("Synthetic room", "room@example.test", "NoResponseReceived")})
	e, notes, err := MapEvent("A", o)
	want := `[{"name":"Synthetic required","address":"required@example.test","type":"required","response":"accepted"},{"name":"Synthetic optional","address":"optional@example.test","type":"optional","response":"tentative"},{"name":"Synthetic room","address":"room@example.test","type":"resource","response":"none"}]`
	if err != nil || notes != (Notes{}) || e.AttendeesJSON != want || unknown(e, calendar.FieldAttendees) {
		t.Fatalf("attendees contract: %q %+v %v", e.AttendeesJSON, notes, err)
	}
	rooms := calendar.Rooms(e)
	if len(rooms) != 1 || rooms[0].Name != "Synthetic room" || rooms[0].Kind != calendar.RoomResource || rooms[0].Address != "room@example.test" {
		t.Fatal("resource attendee did not reach the shared room reader")
	}
	calendar.AssertEveryFieldClassified(t, e)
	for _, key := range []string{"RequiredAttendees", "OptionalAttendees", "Resources"} {
		replace(o, key, []any{})
	}
	e, notes, err = MapEvent("A", o)
	if err != nil || notes != (Notes{}) || e.AttendeesJSON != "[]" || unknown(e, calendar.FieldAttendees) {
		t.Fatal("explicit empty role arrays became unknown or null")
	}
}

func TestMapEventIncompleteRoleListsStayUnknown(t *testing.T) {
	for _, value := range []any{nil, []any{}, []any{attendee("Synthetic", "synthetic@example.test", "Accept")}} {
		o := event()
		replace(o, "RequiredAttendees", value)
		e, notes, err := MapEvent("A", o)
		want := 1
		if value == nil {
			want = 0
		}
		if err != nil || notes.PartialAttendees != want || e.AttendeesJSON != "" || !unknown(e, calendar.FieldAttendees) {
			t.Fatal("partial list claimed a complete attendee unit")
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
}

func TestMapEventDamagedAttendeeListHasNoPartialWinner(t *testing.T) {
	for _, invalid := range []any{
		"private-not-an-array",
		[]any{"private-not-an-attendee"},
		[]any{object("Mailbox", "private-not-a-mailbox")},
		[]any{object("Mailbox", object())},
		[]any{object("Mailbox", object("Name", 42.0))},
		[]any{object("Mailbox", object("Name", "Synthetic", "Name", "conflict"))},
	} {
		o := event()
		replace(o, "RequiredAttendees", []any{attendee("Synthetic good", "good@example.test", "Accept")})
		replace(o, "OptionalAttendees", invalid)
		replace(o, "Resources", []any{})
		e, notes, err := MapEvent("A", o)
		if err != nil || notes.Unmapped != 1 || e.AttendeesJSON != "" || !unknown(e, calendar.FieldAttendees) {
			t.Fatalf("damaged role list erased uncertainty: %+v %v", notes, err)
		}
		calendar.AssertEveryFieldClassified(t, e)
	}
}

func TestMapEventAttendeeResponseUnknownAndScrubAreExplicit(t *testing.T) {
	for _, response := range []any{"Decline", "Organizer", "private-future-response", nil, 42.0} {
		o := event()
		a := object("Mailbox", object("EmailAddress", "synthetic@example.test"), "ResponseType", response)
		replace(o, "RequiredAttendees", []any{a})
		replace(o, "OptionalAttendees", []any{})
		replace(o, "Resources", []any{})
		e, notes, err := MapEvent("A", o)
		if err != nil || e.AttendeesJSON == "" {
			t.Fatal("missing response erased a stated attendee")
		}
		var rows []map[string]string
		if err := json.Unmarshal([]byte(e.AttendeesJSON), &rows); err != nil || len(rows) != 1 || rows[0]["address"] != "synthetic@example.test" {
			t.Fatal("attendee identity changed")
		}
		if response == "Decline" && rows[0]["response"] != "declined" || response == "Organizer" && rows[0]["response"] != "organizer" {
			t.Fatal("explicit attendee response changed")
		}
		if (response == "private-future-response" || response == 42.0) && (notes.Unmapped != 1 || rows[0]["response"] != "") {
			t.Fatal("unknown response became a vote")
		}
	}
	o := event()
	replace(o, "RequiredAttendees", []any{attendee("https://example.test?sig=synthetic-secret", "", "Accept")})
	replace(o, "OptionalAttendees", []any{})
	replace(o, "Resources", []any{})
	e, notes, err := MapEvent("A", o)
	if err != nil || notes.Redacted == 0 || strings.Contains(e.AttendeesJSON, "synthetic-secret") {
		t.Fatal("attendee credential-shaped value survived")
	}
}

func TestMapEventExplicitFlagsAndSmallCachedFields(t *testing.T) {
	o := event()
	replace(o, "IsOnlineMeeting", true)
	replace(o, "IsOrganizer", false)
	replace(o, "HasAttachments", true)
	replace(o, "FreeBusyType", "Busy")
	replace(o, "OnlineMeetingConferenceId", "synthetic-conference")
	replace(o, "OnlineMeetingTollNumber", "synthetic-dial-in")
	e, notes, err := MapEvent("A", o)
	if err != nil || notes != (Notes{}) || !e.IsOnlineMeeting.Is(true) || !e.IsOrganizer.Is(false) || !e.HasAttachments.Is(true) ||
		e.ShowAs != "busy" || e.DialInConferenceID != "synthetic-conference" || e.DialInTollNumber != "synthetic-dial-in" {
		t.Fatal("explicit cached flags or small fields were lost")
	}
	calendar.AssertEveryFieldClassified(t, e)
	for _, tc := range []struct{ input, want string }{{"Free", "free"}, {"Tentative", "tentative"}, {"private-future", ""}} {
		replace(o, "FreeBusyType", tc.input)
		e, notes, err = MapEvent("A", o)
		if err != nil || e.ShowAs != tc.want || (tc.want == "" && notes.Unmapped != 1) {
			t.Fatal("free-busy state was guessed")
		}
	}
	if !reflect.DeepEqual(e.Unknown, calendar.NormalizeUnknown(e.Unknown)) {
		t.Fatal("detail mapping changed unknown vocabulary normalization")
	}
}

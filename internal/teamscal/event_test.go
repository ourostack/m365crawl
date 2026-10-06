package teamscal

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

var testAcct = teamsdesktop.Account{TenantID: "tenant-1", UserID: "user-1", Locale: "en-us"}

const richEvent = `{
 "objectId":"obj-rich","iCalUID":"UID-RICH","cleanGlobalObjectId":"SERIES-RICH","eventType":"Single",
 "startTime":{"$date":"2026-03-10T17:00:00.000Z"},"endTime":{"$date":"2026-03-10T18:00:00.000Z"},
 "lastModifiedTime":{"$date":"2026-03-01T09:30:00.000Z"},"eventTimeZone":"PacificSt","utcOffset":"-08:00:00",
 "subject":"Fixture Planning Sync","organizerName":"Alex Fixture","organizerAddress":"alex@example.test",
 "isOrganizer":true,"isPrivate":true,"isCancelled":false,"isAllDayEvent":false,
 "myResponseType":"Organizer","showAs":"Busy","isOnlineMeeting":true,
 "location":"Fixture Room Alpha; Fixture Room Beta",
 "skypeTeamsMeetingUrl":"https://teams.example.test/l/meetup-join/19%3ameeting_FIXTUREurl%40thread.v2/0?context=x",
 "shortOnlineMeetingJoinUrl":"https://teams.example.test/meet/123",
 "onlineMeetingConferenceId":"123456","onlineMeetingTollNumber":"+1 555 0100",
 "skypeTeamsDataObject":{"cid":"19:meeting_FIXTUREcid@thread.v2"},
 "attendees":[
  {"name":"Alex Fixture","address":"alex@example.test","type":"Organizer","role":"Chair","status":{"response":"Accepted"}},
  {"name":"Blake Fixture","address":"blake@example.test","type":"Optional","role":"Attendee","status":{"response":"NotResponded"}},
  {"name":"Fixture Room Alpha","address":"alpha@example.test","type":"Resource","status":"Tentative"},
  {"name":"","address":""},
  "not an object"
 ],
 "meetingLocations":[
  {"displayName":"Fixture Room Alpha","locationType":"conferenceRoom","address":"1 Example Way","coordinates":{"latitude":47.5,"longitude":-122.25}},
  {"displayName":"Fixture Room Beta","address":{"street":"2 Example Way","city":"Faketown","state":"","postalCode":"00000"}},
  {"displayName":"","address":""}
 ],
 "bodyContent":"<p>Hello&nbsp;team</p>","bodyContentType":"html","bodyPreview":"Hello team",
 "hasAttachments":true,
 "attachments":[{"id":"att-1","name":"agenda.txt","fileName":"ignored.txt","size":12,"contentType":"text/plain","contentId":"cid-1","isInline":true,"attachmentType":"file"},{"id":"att-2","fileName":"b.txt"},{}],
 "categories":["Blue","Fixture",""],"isReminderSet":true,"reminderMinutesBeforeStart":15,
 "recurrencePattern":{"type":"weekly","interval":1},"eventRecurrenceRange":{"type":"endDate"},"recurrenceEnd":{"$date":"2026-12-31T00:00:00.000Z"}
}`

// withTimes adds a valid start and end to a test record that lacks them, because a record with no
// usable times is unmapped.
func withTimes(js string) string {
	add := ""
	if !strings.Contains(js, `"startTime"`) {
		add += `"startTime":{"$date":"2026-03-10T17:00:00.000Z"},`
	}
	if !strings.Contains(js, `"endTime"`) {
		add += `"endTime":{"$date":"2026-03-10T18:00:00.000Z"},`
	}
	return strings.Replace(js, "{", "{"+add, 1)
}

func mapEvent(t *testing.T, key, js string) (calendar.Event, MapNotes) {
	t.Helper()
	e, notes, err := MapEventRecord(testAcct, key, []byte(withTimes(js)), time.UTC)
	if err != nil {
		t.Fatalf("MapEventRecord: %v", err)
	}
	return e, notes
}

func TestMapEventFieldByField(t *testing.T) {
	e, notes := mapEvent(t, "obj-rich", richEvent)
	if notes != (MapNotes{}) {
		t.Errorf("notes = %+v, want none", notes)
	}
	start, end, mod := utcTime("2026-03-10T17:00:00Z"), utcTime("2026-03-10T18:00:00Z"), utcTime("2026-03-01T09:30:00Z")
	reminder := 15
	checks := []struct {
		name      string
		got, want any
	}{
		{"Source", e.Source, calendar.SourceTeams},
		{"AccountID", e.AccountID, "tenant-1/user-1"},
		{"SourceID", e.SourceID, "obj-rich"},
		{"GlobalID", e.GlobalID, "UID-RICH"},
		{"ICalUID", e.ICalUID, "UID-RICH"},
		{"SeriesKey", e.SeriesKey, "SERIES-RICH"},
		{"EventType", e.EventType, calendar.EventSingle},
		{"OriginalStart", e.OriginalStart, (*time.Time)(nil)},
		{"Start", e.Start, start},
		{"End", e.End, end},
		{"AllDay", e.AllDay, calendar.TriFalse},
		{"StartDate", e.StartDate, ""},
		{"EndDate", e.EndDate, ""},
		{"TimeZone", e.TimeZone, "PacificSt"},
		{"TimeZoneIANA", e.TimeZoneIANA, "America/Los_Angeles"},
		{"UTCOffset", e.UTCOffset, "-08:00:00"},
		{"Subject", e.Subject, "Fixture Planning Sync"},
		{"Organizer", e.Organizer, "Alex Fixture"},
		{"OrganizerAddress", e.OrganizerAddress, "alex@example.test"},
		{"IsOrganizer", e.IsOrganizer, calendar.TriTrue},
		{"IsPrivate", e.IsPrivate, calendar.TriTrue},
		{"Cancelled", e.Cancelled, calendar.TriFalse},
		{"Response", e.Response, "organizer"},
		{"ShowAs", e.ShowAs, "busy"},
		{"IsOnlineMeeting", e.IsOnlineMeeting, calendar.TriTrue},
		{"Location", e.Location, "Fixture Room Alpha; Fixture Room Beta"},
		{"LastModified", e.LastModified, &mod},
		{"OnlineMeetingURL", e.OnlineMeetingURL, "https://teams.example.test/l/meetup-join/19%3ameeting_FIXTUREurl%40thread.v2/0?context=x"},
		{"ShortJoinURL", e.ShortJoinURL, "https://teams.example.test/meet/123"},
		{"DialInConferenceID", e.DialInConferenceID, "123456"},
		{"DialInTollNumber", e.DialInTollNumber, "+1 555 0100"},
		{"TeamsThreadID", e.TeamsThreadID, "19:meeting_FIXTUREcid@thread.v2"},
		{"AttendeesJSON", e.AttendeesJSON, `[{"name":"Alex Fixture","address":"alex@example.test","type":"Organizer","role":"Chair","response":"accepted"},` +
			`{"name":"Blake Fixture","address":"blake@example.test","type":"Optional","role":"Attendee","response":"none"},` +
			`{"name":"Fixture Room Alpha","address":"alpha@example.test","type":"Resource","role":"","response":"tentative"}]`},
		{"LocationsJSON", e.LocationsJSON, `[{"name":"Fixture Room Alpha","kind":"conferenceRoom","address":"1 Example Way","latitude":47.5,"longitude":-122.25},` +
			`{"name":"Fixture Room Beta","kind":"","address":"2 Example Way, Faketown, 00000"}]`},
		{"BodyHTML", e.BodyHTML, "<p>Hello&nbsp;team</p>"},
		{"BodyText", e.BodyText, teamsdesktop.HTMLToText("<p>Hello&nbsp;team</p>")},
		{"BodyType", e.BodyType, "html"},
		{"BodyPreview", e.BodyPreview, "Hello team"},
		{"HasAttachments", e.HasAttachments, calendar.TriTrue},
		{"AttachmentsJSON", e.AttachmentsJSON, `[{"id":"att-1","name":"agenda.txt","size":12,"content_type":"text/plain","content_id":"cid-1","is_inline":true,"attachment_type":"file"},` +
			`{"id":"att-2","name":"b.txt","size":0,"content_type":"","content_id":"","is_inline":false,"attachment_type":""}]`},
		{"CategoriesJSON", e.CategoriesJSON, `["Blue","Fixture"]`},
		{"RecurrenceJSON", e.RecurrenceJSON, `{"eventRecurrenceRange":{"type":"endDate"},"recurrenceEnd":{"$date":"2026-12-31T00:00:00.000Z"},"recurrencePattern":{"interval":1,"type":"weekly"}}`},
		{"ReminderMinutes", e.ReminderMinutes, &reminder},
		{"DetailRawJSON", e.DetailRawJSON, richEvent},
		{"DetailAsOf", e.DetailAsOf, (*time.Time)(nil)},
		{"RemovedAt", e.RemovedAt, (*time.Time)(nil)},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	if !strings.Contains(e.BodyText, "Hello") {
		t.Errorf("BodyText = %q", e.BodyText)
	}
}

func TestMapEventRichFeedsRooms(t *testing.T) {
	e, _ := mapEvent(t, "obj-rich", richEvent)
	rooms := calendar.Rooms(e)
	if len(rooms) != 2 || rooms[0].Name != "Fixture Room Alpha" || rooms[0].Kind == calendar.RoomText || rooms[0].Latitude == nil {
		t.Fatalf("rooms = %+v", rooms)
	}
}

func TestSourceIDIsRecordKey(t *testing.T) {
	e, _ := mapEvent(t, "key-wins", `{"objectId":"something-else","iCalUID":"U1"}`)
	if e.SourceID != "key-wins" {
		t.Fatalf("SourceID = %q, want the record key", e.SourceID)
	}
}

func TestMapEventThinRecord(t *testing.T) {
	e, notes := mapEvent(t, "thin", `{"objectId":"thin","iCalUID":"U-THIN","subject":"Thin","startTime":1773162000000,"endTime":"1773165600000","hasAttachments":"true"}`)
	if !notes.EventTypeAbsent || e.EventType != calendar.EventSingle {
		t.Errorf("absent event type: type %q notes %+v", e.EventType, notes)
	}
	if !e.HasAttachments.Is(true) || e.AttendeesJSON != "" || e.LocationsJSON != "" || e.AttachmentsJSON != "" || e.CategoriesJSON != "" || e.RecurrenceJSON != "" ||
		e.ReminderMinutes != nil || e.BodyHTML != "" || e.BodyText != "" || e.TeamsThreadID != "" || e.LastModified != nil || e.TimeZoneIANA != "" {
		t.Errorf("thin record has detail: %+v", e)
	}
	if !e.Start.Equal(utcTime("2026-03-10T17:00:00Z")) || !e.End.Equal(utcTime("2026-03-10T18:00:00Z")) {
		t.Errorf("epoch times: %v %v", e.Start, e.End)
	}
}

func TestMapEventTypes(t *testing.T) {
	cases := map[string]string{
		"Single": "single", "Occurrence": "occurrence", "Exception": "exception", "RecurringMaster": "master", "Strange": "strange",
	}
	for in, want := range cases {
		e, notes := mapEvent(t, "k", `{"iCalUID":"U","eventType":"`+in+`"}`)
		if e.EventType != want || notes.EventTypeAbsent {
			t.Errorf("%s -> %q (notes %+v), want %q", in, e.EventType, notes, want)
		}
	}
}

func TestMapEventResponseNormalized(t *testing.T) {
	cases := map[string]string{
		"Accepted": "accepted", "Tentative": "tentative", "TentativelyAccepted": "tentative", "Declined": "declined",
		"NotResponded": "none", "None": "none", "Organizer": "organizer", "Weird": "weird", "": "",
	}
	for in, want := range cases {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U","myResponseType":"`+in+`"}`)
		if e.Response != want {
			t.Errorf("response %q -> %q, want %q", in, e.Response, want)
		}
	}
}

func TestMapEventCancelledDeclined(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","isCancelled":true,"myResponseType":"Declined"}`)
	if !e.Cancelled.Is(true) || e.Response != "declined" {
		t.Fatalf("cancelled %v response %q", e.Cancelled, e.Response)
	}
}

func TestMapEventThreadIDFallbackFromURL(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","skypeTeamsMeetingUrl":"https://teams.example.test/l/meetup-join/19%3ameeting_FIXTUREurl%40thread.v2/0?context=x"}`)
	if e.TeamsThreadID != "19:meeting_FIXTUREurl@thread.v2" {
		t.Fatalf("TeamsThreadID = %q", e.TeamsThreadID)
	}
}

func TestMapEventThreadIDFromStringForms(t *testing.T) {
	// The data object arrives as an object, as a JSON string, or under skypeTeamsData.
	forms := []string{
		`"skypeTeamsDataObject":"{\"cid\":\"19:meeting_AAA@thread.v2\"}"`,
		`"skypeTeamsData":"{\"cid\":\"19:meeting_AAA@thread.v2\"}"`,
		`"skypeTeamsDataObject":{"cid":"19:meeting_AAA@thread.v2"},"skypeTeamsData":"{\"cid\":\"19:meeting_BBB@thread.v2\"}"`,
	}
	for _, f := range forms {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U",`+f+`}`)
		if e.TeamsThreadID != "19:meeting_AAA@thread.v2" {
			t.Errorf("%s -> %q", f, e.TeamsThreadID)
		}
	}
}

func TestMapEventArrayAsJSONString(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","categories":"[\"A\",\"B\"]","attendees":"[{\"name\":\"N\",\"address\":\"n@example.test\"}]"}`)
	if e.CategoriesJSON != `["A","B"]` || !strings.Contains(e.AttendeesJSON, `"name":"N"`) {
		t.Fatalf("categories %q attendees %q", e.CategoriesJSON, e.AttendeesJSON)
	}
	e, _ = mapEvent(t, "k", `{"iCalUID":"U","categories":"[broken","attendees":"not an array","skypeTeamsDataObject":"{broken"}`)
	if e.CategoriesJSON != "" || e.AttendeesJSON != "" || e.TeamsThreadID != "" {
		t.Fatalf("broken strings produced data: %+v", e)
	}
}

func TestMapEventTextBody(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","bodyContent":"a <b> c","bodyContentType":"Text"}`)
	if e.BodyText != "a <b> c" || e.BodyHTML != "a <b> c" || e.BodyType != "Text" {
		t.Fatalf("text body: %q / %q / %q", e.BodyHTML, e.BodyText, e.BodyType)
	}
}

func TestHTMLBodyToText(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","bodyContent":"<div>One</div><div>Two &amp; three</div>"}`)
	want := teamsdesktop.HTMLToText("<div>One</div><div>Two &amp; three</div>")
	if e.BodyText != want || !strings.Contains(e.BodyText, "Two & three") || e.BodyType != "" {
		t.Fatalf("BodyText = %q (want %q), type %q", e.BodyText, want, e.BodyType)
	}
}

func TestMapEventZoneJunkIsUnknown(t *testing.T) {
	for _, name := range []string{"Factory", "EST5EDT", "america/new_york", "Local", "Europe/Nowhere"} {
		e, notes := mapEvent(t, "k", `{"iCalUID":"U","eventTimeZone":"`+name+`"}`)
		if e.TimeZoneIANA != "" || notes.UnknownZone != name {
			t.Errorf("%s -> %q, %+v", name, e.TimeZoneIANA, notes)
		}
	}
}

func TestMapEventUnknownZone(t *testing.T) {
	e, notes := mapEvent(t, "k", `{"iCalUID":"U","eventTimeZone":"FixtureUnknownSt"}`)
	if notes.UnknownZone != "FixtureUnknownSt" || e.TimeZone != "FixtureUnknownSt" || e.TimeZoneIANA != "" {
		t.Fatalf("notes %+v zone %q iana %q", notes, e.TimeZone, e.TimeZoneIANA)
	}
	_, notes = mapEvent(t, "k", `{"iCalUID":"U"}`)
	if notes.UnknownZone != "" {
		t.Fatalf("no zone reported as unknown: %+v", notes)
	}
	e, notes = mapEvent(t, "k", `{"iCalUID":"U","eventTimeZone":"Europe/Paris"}`)
	if e.TimeZoneIANA != "Europe/Paris" || notes.UnknownZone != "" {
		t.Fatalf("direct IANA: %q %+v", e.TimeZoneIANA, notes)
	}
}

func TestMapEventAllDayShapes(t *testing.T) {
	cases := []struct {
		name, js, fallback string
		allDay             bool
		startDate, endDate string
		unaligned          bool
	}{
		{"utc midnight", `"eventTimeZone":"Utc","startTime":{"$date":"2026-03-10T00:00:00.000Z"},"endTime":{"$date":"2026-03-11T00:00:00.000Z"}`, "", true, "2026-03-10", "2026-03-11", false},
		{"pacific midnight", `"eventTimeZone":"PacificSt","startTime":{"$date":"2026-01-10T08:00:00.000Z"},"endTime":{"$date":"2026-01-11T08:00:00.000Z"}`, "", true, "2026-01-10", "2026-01-11", false},
		{"three days", `"eventTimeZone":"Utc","startTime":{"$date":"2026-03-10T00:00:00.000Z"},"endTime":{"$date":"2026-03-13T00:00:00.000Z"}`, "", true, "2026-03-10", "2026-03-13", false},
		{"unknown zone with utc midnight", `"eventTimeZone":"FixtureUnknownSt","startTime":{"$date":"2026-03-10T00:00:00.000Z"},"endTime":{"$date":"2026-03-11T00:00:00.000Z"}`, "", true, "2026-03-10", "2026-03-11", false},
		{"unaligned falls back to timed", `"eventTimeZone":"PacificSt","startTime":{"$date":"2026-03-10T10:30:00.000Z"},"endTime":{"$date":"2026-03-10T11:30:00.000Z"}`, "", false, "", "", true},
		{"known zone ignores the sync zone", `"eventTimeZone":"PacificSt","startTime":{"$date":"2026-03-09T15:00:00.000Z"},"endTime":{"$date":"2026-03-10T15:00:00.000Z"}`, "Asia/Tokyo", false, "", "", true},
		{"unknown zone, sync zone midnight", `"eventTimeZone":"FixtureUnknownSt","startTime":{"$date":"2026-03-09T15:00:00.000Z"},"endTime":{"$date":"2026-03-10T15:00:00.000Z"}`, "Asia/Tokyo", true, "2026-03-10", "2026-03-11", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := time.UTC
			if c.fallback != "" {
				fb, _ = time.LoadLocation(c.fallback)
			}
			e, notes, err := MapEventRecord(testAcct, "k", []byte(`{"iCalUID":"U","isAllDayEvent":true,`+c.js+`}`), fb)
			if err != nil {
				t.Fatal(err)
			}
			if e.AllDay.Is(true) != c.allDay || e.StartDate != c.startDate || e.EndDate != c.endDate || notes.AllDayUnaligned != c.unaligned {
				t.Fatalf("allDay %v %q..%q unaligned %v; want %v %q..%q %v", e.AllDay, e.StartDate, e.EndDate, notes.AllDayUnaligned, c.allDay, c.startDate, c.endDate, c.unaligned)
			}
		})
	}
}

func TestMapEventAllDayNilSyncZone(t *testing.T) {
	e, notes, err := MapEventRecord(testAcct, "k", []byte(`{"iCalUID":"U","isAllDayEvent":true,"startTime":{"$date":"2026-03-10T10:00:00.000Z"},"endTime":{"$date":"2026-03-10T11:00:00.000Z"}}`), nil)
	if err != nil || e.AllDay.Is(true) || !notes.AllDayUnaligned {
		t.Fatalf("err %v allDay %v notes %+v", err, e.AllDay, notes)
	}
}

func TestMapEventUnmappedRecord(t *testing.T) {
	for name, tc := range map[string]struct{ key, js string }{
		"no key":     {"", `{"iCalUID":"U"}`},
		"bad json":   {"k", `{"iCalUID":`},
		"not object": {"k", `["a"]`},
		"null":       {"k", `null`},
		"trailing":   {"k", `{} {}`},
	} {
		_, _, err := MapEventRecord(testAcct, tc.key, []byte(tc.js), time.UTC)
		var um *UnmappedError
		if !errors.As(err, &um) || um.Reason == "" || !strings.Contains(err.Error(), um.Reason) {
			t.Errorf("%s: err = %v, want *UnmappedError", name, err)
		}
	}
}

func TestMapEventTimeForms(t *testing.T) {
	cases := []struct{ v, want string }{
		{`{"$date":"2026-03-10T17:00:00.000Z"}`, "2026-03-10T17:00:00Z"},
		{`"2026-03-10T17:00:00Z"`, "2026-03-10T17:00:00Z"},
		{`"2026-03-10T17:00:00+02:00"`, "2026-03-10T15:00:00Z"},
		{`1773162000000`, "2026-03-10T17:00:00Z"},
		{`" 1773162000000 "`, "2026-03-10T17:00:00Z"},
	}
	for _, c := range cases {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U","startTime":`+c.v+`}`)
		if !e.Start.Equal(utcTime(c.want)) || e.Start.Location() != time.UTC {
			t.Errorf("startTime %s -> %v, want %s", c.v, e.Start, c.want)
		}
	}
}

func TestMapEventRequiresUsableTimes(t *testing.T) {
	good := `"2026-03-10T17:00:00Z"`
	bad := []string{`{"$date":null}`, `"2026-03-10T17:00:00"`, `"2026-03-10T17:00:00.000"`, `"not a time"`, `5`, `"5"`, `true`, `null`, `{}`}
	for _, b := range bad {
		for _, js := range []string{
			`{"iCalUID":"U","startTime":` + b + `,"endTime":` + good + `}`,
			`{"iCalUID":"U","startTime":` + good + `,"endTime":` + b + `}`,
		} {
			_, _, err := MapEventRecord(testAcct, "k", []byte(js), time.UTC)
			var um *UnmappedError
			if !errors.As(err, &um) {
				t.Errorf("%s: err = %v, want *UnmappedError", js, err)
			}
		}
	}
	for name, js := range map[string]string{
		"no times":           `{"iCalUID":"U","subject":"x"}`,
		"numeric start only": `{"iCalUID":"U","startTime":1773162000000}`,
		"end only":           `{"iCalUID":"U","endTime":` + good + `}`,
		"end before start":   `{"iCalUID":"U","startTime":"2026-03-10T18:00:00Z","endTime":"2026-03-10T17:00:00Z"}`,
	} {
		_, _, err := MapEventRecord(testAcct, "k", []byte(js), time.UTC)
		var um *UnmappedError
		if !errors.As(err, &um) {
			t.Errorf("%s: err = %v, want *UnmappedError", name, err)
		}
	}
	// A zero-length event is allowed.
	if _, _, err := MapEventRecord(testAcct, "k", []byte(`{"iCalUID":"U","startTime":`+good+`,"endTime":`+good+`}`), time.UTC); err != nil {
		t.Errorf("zero-length event: %v", err)
	}
}

func TestMapEventScalarForms(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":12345,"subject":"S","isReminderSet":true,"reminderMinutesBeforeStart":"x","isOrganizer":"TRUE","isPrivate":"no","hasAttachments":1}`)
	if e.ICalUID != "12345" || !e.IsOrganizer.Is(true) || e.IsPrivate.Known() || e.HasAttachments.Known() || e.ReminderMinutes != nil {
		t.Fatalf("scalar forms: %+v", e)
	}
	e, _ = mapEvent(t, "k", `{"iCalUID":"U","isReminderSet":true,"reminderMinutesBeforeStart":7.0}`)
	if e.ReminderMinutes == nil || *e.ReminderMinutes != 7 {
		t.Fatalf("float reminder: %v", e.ReminderMinutes)
	}
	e, _ = mapEvent(t, "k", `{"iCalUID":"U","iCalUID2":1,"isReminderSet":true,"reminderMinutesBeforeStart":0}`)
	if e.ReminderMinutes == nil || *e.ReminderMinutes != 0 {
		t.Fatalf("zero reminder must be kept: %v", e.ReminderMinutes)
	}
}

func TestMapEventOddDetailShapes(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","attendees":[{"name":"A","address":"a@example.test","status":{"response":"Declined"}}],
	  "meetingLocations":[{"displayName":"Room","coordinates":{"latitude":"x"}},{"displayName":"Room2","address":{"city":""}}],
	  "attachments":"[{\"name\":\"n\",\"size\":2.5}]","recurrencePattern":"{\"type\":\"daily\"}"}`)
	if !strings.Contains(e.AttendeesJSON, `"response":"declined"`) {
		t.Errorf("attendees %q", e.AttendeesJSON)
	}
	if e.LocationsJSON != `[{"name":"Room","kind":"","address":""},{"name":"Room2","kind":"","address":""}]` {
		t.Errorf("locations %q", e.LocationsJSON)
	}
	if !strings.Contains(e.AttachmentsJSON, `"name":"n"`) || !strings.Contains(e.AttachmentsJSON, `"size":2`) {
		t.Errorf("attachments %q", e.AttachmentsJSON)
	}
	if e.RecurrenceJSON != `{"recurrencePattern":{"type":"daily"}}` {
		t.Errorf("recurrence %q", e.RecurrenceJSON)
	}
}

func TestJoinURLSurvivesScrub(t *testing.T) {
	// A representative synthetic join link: context parameters hold JSON-looking ids, which the
	// scrubber must leave alone.
	join := "https://teams.example.test/l/meetup-join/19%3ameeting_FIXTUREscrub%40thread.v2/0?context=%7b%22Tid%22%3a%2200000000-0000-0000-0000-000000000001%22%2c%22Oid%22%3a%2200000000-0000-0000-0000-000000000002%22%7d&anon=true"
	in := `{"objectId":"o","iCalUID":"U","skypeTeamsMeetingUrl":"` + join + `","shortOnlineMeetingJoinUrl":"https://teams.example.test/meet/1?p=abc"}`
	out, n := teamsdesktop.Scrub([]byte(in))
	if n != 0 || string(out) != in {
		t.Fatalf("Scrub changed the record (%d redactions): %s", n, out)
	}
	e, _ := mapEvent(t, "o", string(out))
	if e.OnlineMeetingURL != join || strings.Contains(e.OnlineMeetingURL, "[redacted]") {
		t.Fatalf("join url = %q", e.OnlineMeetingURL)
	}
}

func TestMapEventReminderStates(t *testing.T) {
	cases := []struct {
		name, js string
		minutes  *int
		stated   bool
	}{
		{"set with minutes", `"isReminderSet":true,"reminderMinutesBeforeStart":10`, ptrInt(10), true},
		{"set with zero", `"isReminderSet":true,"reminderMinutesBeforeStart":0`, ptrInt(0), true},
		{"set without minutes", `"isReminderSet":true`, nil, false},
		{"not set", `"isReminderSet":false,"reminderMinutesBeforeStart":15`, nil, true},
		{"absent key", `"reminderMinutesBeforeStart":15`, nil, false},
		{"null key", `"isReminderSet":null`, nil, false},
	}
	for _, c := range cases {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U",`+c.js+`}`)
		if !reflect.DeepEqual(e.ReminderMinutes, c.minutes) || containsField(e.Unknown, calendar.FieldReminder) == c.stated {
			t.Errorf("%s: minutes %v unknown %v", c.name, e.ReminderMinutes, e.Unknown)
		}
	}
}

func TestMapEventReminderRange(t *testing.T) {
	for js, ok := range map[string]bool{`-1`: false, `40321`: false, `40320`: true, `0`: true} {
		e, notes := mapEvent(t, "k", `{"iCalUID":"U","isReminderSet":true,"reminderMinutesBeforeStart":`+js+`}`)
		if ok && (e.ReminderMinutes == nil || containsField(e.Unknown, calendar.FieldReminder) || notes.ReminderOutOfRange) {
			t.Errorf("%s: %v %v %+v", js, e.ReminderMinutes, e.Unknown, notes)
		}
		if !ok && (e.ReminderMinutes != nil || !containsField(e.Unknown, calendar.FieldReminder) || !notes.ReminderOutOfRange) {
			t.Errorf("%s: %v %v %+v", js, e.ReminderMinutes, e.Unknown, notes)
		}
	}
}

func TestMapEventMissingICalUID(t *testing.T) {
	e, notes := mapEvent(t, "k", `{"subject":"no uid"}`)
	if !notes.MissingICalUID || e.GlobalID != "" || e.ICalUID != "" {
		t.Fatalf("notes %+v event %+v", notes, e)
	}
	if _, notes = mapEvent(t, "k", `{"iCalUID":"U"}`); notes.MissingICalUID {
		t.Fatal("present uid reported missing")
	}
}

func ptrInt(n int) *int { return &n }

func TestMapEventCategoriesStates(t *testing.T) {
	cases := map[string]string{
		`"categories":[]`:           "[]",
		`"categories":[""]`:         "[]",
		`"categories":["A"]`:        `["A"]`,
		`"categories":null`:         "",
		`"subject":"no categories"`: "",
	}
	for js, want := range cases {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U",`+js+`}`)
		if e.CategoriesJSON != want {
			t.Errorf("%s -> %q, want %q", js, e.CategoriesJSON, want)
		}
	}
}

func TestMapCatchUpRecordingFieldsTravelTogether(t *testing.T) {
	recaps, _, _, _ := MapCatchUpRecord(testAcct, "U", []byte(`{"data":[{"callId":"a","url":"https://recordings.example.test/a","duration":60000,"recordingStartTime":"2026-03-10T17:00:00Z"}]}`))
	r := recaps[0]
	if r.RecordingURL == "" || r.RecordingStartAt == nil || r.RecordingEndAt == nil || r.DurationSeconds != 60 {
		t.Fatalf("%+v", r)
	}
}

func TestMapEventOnlineFlagIsKnownOnlyWhenPresent(t *testing.T) {
	for js, want := range map[string]calendar.Tri{`"isOnlineMeeting":true`: calendar.TriTrue, `"isOnlineMeeting":false`: calendar.TriFalse, `"subject":"thin"`: calendar.TriUnknown, `"isOnlineMeeting":null`: calendar.TriUnknown} {
		e, _ := mapEvent(t, "k", `{"iCalUID":"U",`+js+`}`)
		if e.IsOnlineMeeting != want {
			t.Errorf("%s: IsOnlineMeeting %v", js, e.IsOnlineMeeting)
		}
	}
}

func containsField(fields []calendar.Field, f calendar.Field) bool {
	for _, x := range fields {
		if x == f {
			return true
		}
	}
	return false
}

func TestMapEventAbsentKeysAreUnknown(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","startTime":1773162000000,"endTime":1773165600000}`)
	for _, f := range []calendar.Field{calendar.FieldSubject, calendar.FieldLocation, calendar.FieldOrganizer, calendar.FieldAttendees,
		calendar.FieldBody, calendar.FieldRooms, calendar.FieldReminder, calendar.FieldJoinURL, calendar.FieldDialIn, calendar.FieldMeetingChatID,
		calendar.FieldRecurrence, calendar.FieldTimeZone, calendar.FieldTimeZoneIANA, calendar.FieldUTCOffset, calendar.FieldCategories} {
		if !containsField(e.Unknown, f) {
			t.Errorf("%s must be unknown: %v", f, e.Unknown)
		}
	}
	for _, flag := range []calendar.Tri{e.AllDay, e.IsOrganizer, e.IsPrivate, e.Cancelled, e.IsOnlineMeeting, e.HasAttachments} {
		if flag != calendar.TriUnknown {
			t.Errorf("an absent flag must be unknown: %+v", e)
		}
	}
	// A thin record says nothing of its detail, so the output collapses it to one name.
	if got := calendar.UnknownFields(e); !containsString(got, "detail") {
		t.Errorf("%v", got)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestMapEventPresentFalseIsKnown(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","subject":"","location":"","attendees":[],"categories":[],"isOrganizer":false,"isAllDayEvent":false,"isReminderSet":false}`)
	for _, f := range []calendar.Field{calendar.FieldSubject, calendar.FieldLocation, calendar.FieldAttendees, calendar.FieldCategories, calendar.FieldReminder} {
		if containsField(e.Unknown, f) {
			t.Errorf("%s is present, so known: %v", f, e.Unknown)
		}
	}
	if !e.IsOrganizer.Is(false) || !e.AllDay.Is(false) {
		t.Errorf("%+v", e)
	}
	// A null key states nothing.
	n, _ := mapEvent(t, "k", `{"iCalUID":"U","location":null}`)
	if !containsField(n.Unknown, calendar.FieldLocation) {
		t.Errorf("a null location is unknown: %v", n.Unknown)
	}
}

func TestMappedEventsPassValidateEvent(t *testing.T) {
	for _, js := range []string{richEvent, `{"iCalUID":"U"}`,
		`{"iCalUID":"U","isAllDayEvent":true,"startTime":"2026-03-10T00:00:00Z","endTime":"2026-03-11T00:00:00Z"}`} {
		e, _ := mapEvent(t, "k", js)
		if err := calendar.ValidateEvent(e); err != nil {
			t.Errorf("%v", err)
		}
	}
}

func TestMapEventTimeOutsideSupportedYearsIsUnmapped(t *testing.T) {
	_, _, err := MapEventRecord(testAcct, "k", []byte(`{"iCalUID":"U","startTime":"2026-03-10T17:00:00Z","endTime":9999999999999999}`), time.UTC)
	var um *UnmappedError
	if !errors.As(err, &um) {
		t.Errorf("err = %v", err)
	}
}

func TestMapEventLocationTextLeavesStructuredEmpty(t *testing.T) {
	e, _ := mapEvent(t, "k", `{"iCalUID":"U","location":"Fixture Room Alpha"}`)
	if e.LocationsJSON != "" {
		t.Errorf("LocationsJSON synthesized: %q", e.LocationsJSON)
	}
}

func TestMapEventMeetingLinksComeFromOneRecord(t *testing.T) {
	e, _ := mapEvent(t, "k", richEvent)
	if e.OnlineMeetingURL == "" || e.ShortJoinURL == "" || e.DialInConferenceID == "" || e.DialInTollNumber == "" || e.TeamsThreadID == "" || !e.IsOnlineMeeting.Known() {
		t.Errorf("%+v", e)
	}
}

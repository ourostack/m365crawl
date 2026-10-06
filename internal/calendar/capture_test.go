package calendar

import (
	"reflect"
	"testing"
	"time"
)

const (
	t1 = "2026-10-01T10:00:00Z"
	t2 = "2026-10-02T10:00:00Z"
	t3 = "2026-10-03T10:00:00Z"
)

const (
	attendeesTwo   = `[{"name":"Alex Fixture","address":"alex@example.test"},{"name":"Blake Fixture","address":"blake@example.test"}]`
	attendeesThree = `[{"name":"Alex Fixture","address":"alex@example.test"},{"name":"Blake Fixture","address":"blake@example.test"},{"name":"Casey Fixture","address":"casey@example.test"}]`
	attendeesOne   = `[{"name":"Alex Fixture","address":"alex@example.test"}]`
	roomJSON       = `[{"name":"Fixture Room Alpha","kind":"room","address":"alpha@example.test"}]`
)

// thinUnknown is what a thin copy does not say: the whole detail group, which the Teams list view
// leaves out. A field in it is read as known when the copy carries a value for it.
var thinUnknown = []Field{
	FieldJoinURL, FieldShortJoinURL, FieldDialIn, FieldMeetingChatID, FieldAttendees, FieldRooms, FieldBody,
	FieldBodyPreview, FieldAttachments, FieldCategories, FieldRecurrence, FieldReminder,
}

// thin is a copy that carries the schedule and identity only, as Teams caches for an event the
// user has not opened. It states every flag but HasAttachments and every schedule field, and says
// the detail group is unknown.
func thin(t testing.TB, lm string) Event {
	return Event{
		UnknownDeclared: true,
		Source:          SourceTeams, AccountID: "tenant-1/user-1", SourceID: "ev1", GlobalID: "uid-1", ICalUID: "uid-1",
		SeriesKey: "series-1", EventType: EventOccurrence,
		Start: mustTime(t, "2026-10-05T16:00:00Z"), End: mustTime(t, "2026-10-05T17:00:00Z"),
		TimeZone: "PacificSt", Subject: "Fixture Sync", Organizer: "Alex Fixture", Response: "accepted", ShowAs: "busy",
		Location: "Fixture Room Alpha", LastModified: tp(t, lm),
		AllDay: TriFalse, IsOrganizer: TriFalse, IsPrivate: TriFalse, Cancelled: TriFalse, IsOnlineMeeting: TriTrue,
		Unknown: append([]Field{FieldTimeZoneIANA, FieldUTCOffset, FieldOrganizerAddress}, thinUnknown...),
	}
}

// rich is thin plus the detail group, all stamped with lm.
func rich(t testing.TB, lm string) Event {
	e := thin(t, lm)
	e.OnlineMeetingURL = "https://teams.example.test/l/meetup-join/fixture"
	e.ShortJoinURL = "https://teams.example.test/meet/fixture"
	e.DialInConferenceID, e.DialInTollNumber = "123456", "+1 555 0100"
	e.TeamsThreadID = "19:meeting_FIXTURE@thread.v2"
	e.AttendeesJSON = attendeesTwo
	e.LocationsJSON = roomJSON
	e.BodyHTML, e.BodyText, e.BodyType, e.BodyPreview = "<p>Agenda body</p>", "Agenda body", "html", "Agenda"
	e.AttachmentsJSON, e.HasAttachments = `[{"name":"notes.txt"}]`, TriTrue
	e.CategoriesJSON = `["Fixture"]`
	e.RecurrenceJSON = `{"pattern":"weekly"}`
	reminder := 15
	e.ReminderMinutes = &reminder
	e.DetailRawJSON = `{"raw":"` + lm + `"}`
	return e
}

func at(t testing.TB, s string) *time.Time { return tp(t, s) }

func TestCaptureNewEventTakesIncoming(t *testing.T) {
	in := rich(t, t1)
	got := mustCapture(t, nil, in)
	want := in
	want.DetailAsOf = at(t, t1)
	want.Unknown = []Field{FieldOrganizerAddress, FieldTimeZoneIANA, FieldUTCOffset}
	want.UnknownDeclared = false // input only
	// A schedule unit stated at the row's LastModified stores no clock; every other unit does.
	clocks := map[string]time.Time{"raw": *at(t, t1)}
	for _, u := range units {
		if !u.schedule {
			clocks[u.name] = *at(t, t1)
		}
	}
	want.FieldClocksJSON = formatClocks(clocks)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if bare := mustCapture(t, nil, thin(t, t1)); bare.DetailAsOf != nil || bare.DetailRawJSON != "" {
		t.Fatalf("a thin first copy supplies no detail: %+v", bare)
	}
}

func TestCaptureThinnerLaterCopyKeepsDetail(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	got := mustCapture(t, &old, thin(t, t3))
	if got.AttendeesJSON != attendeesTwo || got.BodyHTML != "<p>Agenda body</p>" || got.LocationsJSON != roomJSON ||
		got.OnlineMeetingURL == "" || got.TeamsThreadID == "" || got.HasAttachments != TriTrue || got.ReminderMinutes == nil {
		t.Fatalf("thin copy erased detail: %+v", got)
	}
	if !got.DetailAsOf.Equal(mustTime(t, t1)) || got.DetailRawJSON != old.DetailRawJSON {
		t.Fatalf("detail_as_of moved: %v %s", got.DetailAsOf, got.DetailRawJSON)
	}
	if !got.LastModified.Equal(mustTime(t, t3)) {
		t.Fatalf("last_modified = %v", got.LastModified)
	}
}

func TestCaptureThinnerCopyWithTimeMoveWins(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	moved := thin(t, t3)
	moved.Start, moved.End = mustTime(t, "2026-10-06T16:00:00Z"), mustTime(t, "2026-10-06T17:00:00Z")
	got := mustCapture(t, &old, moved)
	if !got.Start.Equal(moved.Start) || !got.End.Equal(moved.End) {
		t.Fatalf("time move lost: %v", got.Start)
	}
	if got.AttendeesJSON != attendeesTwo || DetailLevel(got) != "stale" {
		t.Fatalf("detail %q level %s", got.AttendeesJSON, DetailLevel(got))
	}
}

func TestCaptureOrderIndependentDetail(t *testing.T) {
	r1, tn3, r2 := rich(t, t1), thin(t, t3), rich(t, t2)
	r1.AttendeesJSON, r2.AttendeesJSON = attendeesTwo, attendeesThree
	r1.BodyHTML, r2.BodyHTML = "<p>old body here</p>", "<p>new body</p>" // newer is shorter
	copies := map[string]Event{"r1": r1, "t3": tn3, "r2": r2}
	orders := [][]string{
		{"r1", "t3", "r2"}, {"r1", "r2", "t3"}, {"t3", "r1", "r2"}, {"t3", "r2", "r1"}, {"r2", "r1", "t3"}, {"r2", "t3", "r1"},
	}
	for _, order := range orders {
		var stored *Event
		for _, name := range order {
			next := mustCapture(t, stored, copies[name])
			stored = &next
		}
		if stored.AttendeesJSON != attendeesThree || stored.BodyHTML != "<p>new body</p>" {
			t.Errorf("%v: detail is not T2's: %q %q", order, stored.AttendeesJSON, stored.BodyHTML)
		}
		if !stored.DetailAsOf.Equal(mustTime(t, t2)) || !stored.LastModified.Equal(mustTime(t, t3)) {
			t.Errorf("%v: detail_as_of %v last_modified %v", order, stored.DetailAsOf, stored.LastModified)
		}
	}
	// Without T2 the detail is T1's.
	for _, order := range [][]string{{"r1", "t3"}, {"t3", "r1"}} {
		var stored *Event
		for _, name := range order {
			next := mustCapture(t, stored, copies[name])
			stored = &next
		}
		if stored.AttendeesJSON != attendeesTwo || !stored.DetailAsOf.Equal(mustTime(t, t1)) || !stored.LastModified.Equal(mustTime(t, t3)) {
			t.Errorf("%v: %q %v %v", order, stored.AttendeesJSON, stored.DetailAsOf, stored.LastModified)
		}
	}
}

func TestCaptureStaleRichCopyFillsOnlyEmptyDetail(t *testing.T) {
	// Stored: schedule and part of the detail at T3.
	cur := rich(t, t3)
	cur.BodyHTML, cur.BodyText, cur.BodyPreview = "", "", ""
	cur.Subject = "Renamed At T3"
	old := mustCapture(t, nil, cur)
	staleRich := rich(t, t1)
	staleRich.AttendeesJSON = attendeesOne
	got := mustCapture(t, &old, staleRich)
	if got.Subject != "Renamed At T3" || !got.LastModified.Equal(mustTime(t, t3)) {
		t.Fatalf("schedule regressed: %q %v", got.Subject, got.LastModified)
	}
	if got.AttendeesJSON != attendeesTwo {
		t.Fatalf("stale copy replaced a non-empty field: %q", got.AttendeesJSON)
	}
	if got.BodyHTML != "<p>Agenda body</p>" {
		t.Fatalf("stale copy did not fill an empty field: %q", got.BodyHTML)
	}
	if !got.DetailAsOf.Equal(mustTime(t, t3)) {
		t.Fatalf("a stale fill must not regress detail_as_of: %v", got.DetailAsOf)
	}
}

func TestCaptureCancelledNewerWins(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	c := thin(t, t3)
	c.Cancelled = TriTrue
	if got := mustCapture(t, &old, c); !got.Cancelled.Is(true) {
		t.Fatal("a newer cancellation must win")
	}
}

func TestCaptureStaleCancelledIgnored(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t3))
	c := thin(t, t1)
	c.Cancelled = TriTrue
	if got := mustCapture(t, &old, c); got.Cancelled.Is(true) {
		t.Fatal("a stale cancellation must be ignored")
	}
}

func TestCaptureUncancelNewerWins(t *testing.T) {
	first := rich(t, t1)
	first.Cancelled = TriTrue
	old := mustCapture(t, nil, first)
	if got := mustCapture(t, &old, thin(t, t3)); got.Cancelled.Is(true) {
		t.Fatal("a newer un-cancel must win")
	}
}

func TestCaptureNewerRichReplacesAttendees(t *testing.T) {
	first := rich(t, t1)
	first.AttendeesJSON = attendeesThree
	old := mustCapture(t, nil, first)
	next := rich(t, t2)
	next.AttendeesJSON = attendeesOne
	got := mustCapture(t, &old, next)
	if got.AttendeesJSON != attendeesOne || !got.DetailAsOf.Equal(mustTime(t, t2)) {
		t.Fatalf("a strictly newer detail copy must replace: %q %v", got.AttendeesJSON, got.DetailAsOf)
	}
}

func TestCapturePeersKeepsLargerListEvenWhenAttendeeRemoved(t *testing.T) {
	// Pins the accepted behavior from the plan: a removal that arrives with an equal or missing
	// modification time cannot be told from a thinner copy, so the larger list stays.
	first := rich(t, t1)
	first.AttendeesJSON = attendeesThree
	old := mustCapture(t, nil, first)
	same := rich(t, t1)
	same.AttendeesJSON = attendeesOne
	if got := mustCapture(t, &old, same); got.AttendeesJSON != attendeesThree {
		t.Fatalf("equal time: %q", got.AttendeesJSON)
	}
	noTime := rich(t, t1)
	noTime.LastModified = nil
	noTime.AttendeesJSON = attendeesOne
	if got := mustCapture(t, &old, noTime); got.AttendeesJSON != attendeesThree {
		t.Fatalf("missing time: %q", got.AttendeesJSON)
	}
	// A bigger peer list wins, and on a tie the incoming copy does.
	bigger := rich(t, t1)
	bigger.AttendeesJSON = attendeesThree + " "
	bigger.LocationsJSON = `[{"name":"A"},{"name":"B"}]`
	if got := mustCapture(t, &old, bigger); got.LocationsJSON != bigger.LocationsJSON {
		t.Fatalf("peer with more locations: %q", got.LocationsJSON)
	}
	tie := rich(t, t1)
	tie.AttendeesJSON = `[{"name":"Dana Fixture"},{"name":"Eli Fixture"},{"name":"Fay Fixture"}]`
	if got := mustCapture(t, &old, tie); got.AttendeesJSON != tie.AttendeesJSON {
		t.Fatalf("tie must take the incoming list: %q", got.AttendeesJSON)
	}
	// A value that is not a JSON array measures as zero, so any real list beats it.
	junk := old
	junk.AttendeesJSON = "not json"
	if got := mustCapture(t, &junk, rich(t, t1)); got.AttendeesJSON != attendeesTwo {
		t.Fatalf("list must beat junk: %q", got.AttendeesJSON)
	}
}

func TestCaptureBodyGrowsAndShrinks(t *testing.T) {
	first := rich(t, t1)
	first.BodyHTML = "<p>a long original body</p>"
	old := mustCapture(t, nil, first)
	short := rich(t, t2)
	short.BodyHTML = "<p>short</p>"
	if got := mustCapture(t, &old, short); got.BodyHTML != "<p>short</p>" {
		t.Fatalf("a newer, shorter body must replace: %q", got.BodyHTML)
	}
	peer := rich(t, t1)
	peer.BodyHTML = "<p>short</p>"
	if got := mustCapture(t, &old, peer); got.BodyHTML != first.BodyHTML {
		t.Fatalf("a peer, shorter body must not replace: %q", got.BodyHTML)
	}
	longer := rich(t, t1)
	longer.BodyHTML = "<p>a long original body with more words</p>"
	if got := mustCapture(t, &old, longer); got.BodyHTML != longer.BodyHTML {
		t.Fatalf("a peer, longer body must replace: %q", got.BodyHTML)
	}
}

func TestCaptureRoomChangeMarksStructuredRoomStale(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	moved := thin(t, t3)
	moved.Location = "Fixture Room Beta"
	got := mustCapture(t, &old, moved)
	if got.LocationsJSON != roomJSON || got.Location != "Fixture Room Beta" {
		t.Fatalf("location %q structured %q", got.Location, got.LocationsJSON)
	}
	rooms := Rooms(got)
	byName := map[string]Room{}
	for _, r := range rooms {
		byName[r.Name] = r
	}
	if r := byName["Fixture Room Alpha"]; r.Kind != "structured" || !r.Stale {
		t.Fatalf("old structured room must be stale: %+v", rooms)
	}
	if r := byName["Fixture Room Beta"]; r.Kind != "text" || r.Stale {
		t.Fatalf("new text room: %+v", rooms)
	}
	if !got.DetailAsOf.Equal(mustTime(t, t1)) {
		t.Fatalf("rooms_as_of = %v", got.DetailAsOf)
	}
}

func TestCaptureOnlineMeetingRemoved(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	off := thin(t, t3)
	off.IsOnlineMeeting = TriFalse
	got := mustCapture(t, &old, off)
	if got.OnlineMeetingURL != "" || got.ShortJoinURL != "" || got.DialInConferenceID != "" || got.DialInTollNumber != "" || got.TeamsThreadID != "" {
		t.Fatalf("meeting links survived removal: %+v", got)
	}
	if got.AttendeesJSON != attendeesTwo {
		t.Fatal("only the meeting links clear")
	}
	// A stale copy that says it is not online does not clear anything.
	cur := mustCapture(t, nil, rich(t, t3))
	staleOff := thin(t, t1)
	staleOff.IsOnlineMeeting = TriFalse
	if got := mustCapture(t, &cur, staleOff); got.OnlineMeetingURL == "" {
		t.Fatal("a stale copy cleared the join link")
	}
	// A newer copy that still carries the link keeps it even if it says not online.
	both := rich(t, t3)
	both.IsOnlineMeeting = TriFalse
	if got := mustCapture(t, &old, both); got.OnlineMeetingURL == "" {
		t.Fatal("the incoming copy's own link was cleared")
	}
	// A copy that merely lacks the URL while still online does not clear it.
	if got := mustCapture(t, &old, thin(t, t3)); got.OnlineMeetingURL == "" {
		t.Fatal("a thin online copy cleared the join link")
	}
}

func TestCaptureEmptyLocationNewerClears(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	c := thin(t, t3)
	c.Location = ""
	if got := mustCapture(t, &old, c); got.Location != "" {
		t.Fatalf("a newer empty location must clear: %q", got.Location)
	}
	stale := thin(t, t1)
	stale.Location = ""
	cur := mustCapture(t, nil, rich(t, t3))
	if got := mustCapture(t, &cur, stale); got.Location != "Fixture Room Alpha" {
		t.Fatalf("a stale empty location must not clear: %q", got.Location)
	}
}

func TestCaptureLastModifiedMissing(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t2))
	in := thin(t, t2)
	in.LastModified = nil
	in.Subject = "Edited Without A Time"
	got := mustCapture(t, &old, in)
	if got.Subject != "Edited Without A Time" || !got.LastModified.Equal(mustTime(t, t2)) {
		t.Fatalf("a missing time is a peer: %q %v", got.Subject, got.LastModified)
	}
	// With no stored time either, the incoming copy is taken and the time stays missing.
	none := thin(t, t2)
	none.LastModified = nil
	first := mustCapture(t, nil, none)
	if first.LastModified != nil || first.DetailAsOf != nil {
		t.Fatalf("%+v", first)
	}
	// Detail that arrives without a time still lands, but cannot claim a clock.
	r := rich(t, t2)
	r.LastModified = nil
	got = mustCapture(t, &first, r)
	if got.AttendeesJSON != attendeesTwo || got.DetailAsOf != nil || DetailLevel(got) != "basic" {
		t.Fatalf("%q %v %s", got.AttendeesJSON, got.DetailAsOf, DetailLevel(got))
	}
}

func TestCaptureIdempotent(t *testing.T) {
	for name, in := range map[string]Event{"rich": rich(t, t2), "thin": thin(t, t2)} {
		once := mustCapture(t, nil, in)
		twice := mustCapture(t, &once, in)
		if !storedEqual(once, twice) {
			t.Errorf("%s: the same copy twice changed the row:\n%+v\n%+v", name, once, twice)
		}
	}
}

func TestCaptureIdentityAndRemoval(t *testing.T) {
	first := rich(t, t1)
	first.EventType = EventException
	first.OriginalStart = at(t, "2026-10-05T16:00:00Z")
	old := mustCapture(t, nil, first)
	removed := mustTime(t, t2)
	old.RemovedAt, old.FirstSeenAt = &removed, mustTime(t, t1)
	blank := thin(t, t3)
	blank.EventType, blank.SeriesKey, blank.ICalUID, blank.OriginalStart = "", "", "", nil
	got := mustCapture(t, &old, blank)
	if got.EventType != EventException || got.SeriesKey != "series-1" || got.ICalUID != "uid-1" || got.OriginalStart == nil {
		t.Fatalf("identity must survive an empty incoming value: %+v", got)
	}
	if got.RemovedAt != nil || !got.FirstSeenAt.Equal(mustTime(t, t1)) {
		t.Fatalf("seeing an event again clears removal and keeps first_seen_at: %v %v", got.RemovedAt, got.FirstSeenAt)
	}
	renamed := thin(t, t3)
	renamed.SeriesKey, renamed.EventType = "series-2", EventSingle
	if got := mustCapture(t, &old, renamed); got.SeriesKey != "series-2" || got.EventType != EventSingle {
		t.Fatalf("a non-empty identity value must win: %+v", got)
	}
}

func TestCaptureReminderAndAttachmentsFollowDetailClock(t *testing.T) {
	first := rich(t, t1)
	old := mustCapture(t, nil, first)
	// Incoming empty keeps.
	if got := mustCapture(t, &old, thin(t, t2)); got.ReminderMinutes == nil || *got.ReminderMinutes != 15 || got.HasAttachments != TriTrue {
		t.Fatalf("empty incoming erased: %+v", got)
	}
	// Newer both-set takes incoming; stale keeps old; peers take incoming.
	five := 5
	newer := rich(t, t2)
	newer.ReminderMinutes = &five
	if got := mustCapture(t, &old, newer); *got.ReminderMinutes != 5 {
		t.Fatalf("newer reminder: %d", *got.ReminderMinutes)
	}
	cur := mustCapture(t, nil, rich(t, t3))
	if got := mustCapture(t, &cur, newer); *got.ReminderMinutes != 15 {
		t.Fatalf("stale reminder: %d", *got.ReminderMinutes)
	}
	// Equal times: the greater stated value wins in either order, never the arrival order.
	tie := rich(t, t1)
	tie.ReminderMinutes = &five
	if got := mustCapture(t, &old, tie); *got.ReminderMinutes != 15 {
		t.Fatalf("tied reminder: %d", *got.ReminderMinutes)
	}
	// A copy with no time cannot be ordered: it competes as a peer and wins.
	peer := rich(t, t1)
	peer.LastModified, peer.ReminderMinutes = nil, &five
	if got := mustCapture(t, &old, peer); *got.ReminderMinutes != 5 {
		t.Fatalf("peer reminder: %d", *got.ReminderMinutes)
	}
	// Filling an empty reminder.
	bare := mustCapture(t, nil, thin(t, t1))
	if got := mustCapture(t, &bare, rich(t, t2)); got.ReminderMinutes == nil || got.HasAttachments != TriTrue {
		t.Fatalf("fill: %+v", got)
	}
}

func TestCaptureThinCopyWithSmallFieldsDoesNotClaimDetailClock(t *testing.T) {
	five := 5
	variants := map[string]func(*Event){
		"reminder":   func(e *Event) { e.ReminderMinutes = &five },
		"categories": func(e *Event) { e.CategoriesJSON = `["Other"]` },
		"both":       func(e *Event) { e.ReminderMinutes = &five; e.CategoriesJSON = `["Other"]` },
	}
	for name, mutate := range variants {
		r1, tn3, r2 := rich(t, t1), thin(t, t3), rich(t, t2)
		mutate(&tn3)
		r2.AttendeesJSON, r2.BodyHTML = attendeesThree, "<p>new body</p>"
		copies := map[string]Event{"r1": r1, "t3": tn3, "r2": r2}
		for _, order := range [][]string{
			{"r1", "t3", "r2"}, {"r1", "r2", "t3"}, {"t3", "r1", "r2"}, {"t3", "r2", "r1"}, {"r2", "r1", "t3"}, {"r2", "t3", "r1"},
		} {
			var stored *Event
			for _, n := range order {
				next := mustCapture(t, stored, copies[n])
				stored = &next
			}
			if stored.AttendeesJSON != attendeesThree || stored.BodyHTML != "<p>new body</p>" {
				t.Errorf("%s %v: detail is not T2's: %q %q", name, order, stored.AttendeesJSON, stored.BodyHTML)
			}
			if !stored.DetailAsOf.Equal(mustTime(t, t2)) || stored.DetailRawJSON != r2.DetailRawJSON {
				t.Errorf("%s %v: detail clock %v raw %q", name, order, stored.DetailAsOf, stored.DetailRawJSON)
			}
			// The newest copy that carries a small field keeps it.
			if tn3.ReminderMinutes != nil && (stored.ReminderMinutes == nil || *stored.ReminderMinutes != 5) {
				t.Errorf("%s %v: reminder %v", name, order, stored.ReminderMinutes)
			}
			if tn3.CategoriesJSON != "" && stored.CategoriesJSON != tn3.CategoriesJSON {
				t.Errorf("%s %v: categories %q", name, order, stored.CategoriesJSON)
			}
		}
	}
}

func TestCaptureEmptyRawDetailNeverOverwrites(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	in := rich(t, t2)
	in.AttendeesJSON, in.DetailRawJSON = attendeesThree, ""
	got := mustCapture(t, &old, in)
	if got.AttendeesJSON != attendeesThree || !got.DetailAsOf.Equal(mustTime(t, t2)) {
		t.Fatalf("substantive copy did not advance: %q %v", got.AttendeesJSON, got.DetailAsOf)
	}
	if got.DetailRawJSON != old.DetailRawJSON {
		t.Fatalf("empty raw detail overwrote the stored one: %q", got.DetailRawJSON)
	}
}

func TestDetailEqualComparesReminders(t *testing.T) {
	five, fifteen := 5, 15
	a, b := thin(t, t1), thin(t, t1)
	a.ReminderMinutes, b.ReminderMinutes = &five, &five
	if !detailEqual(a, b) {
		t.Fatal("equal reminders are equal detail")
	}
	b.ReminderMinutes = &fifteen
	if detailEqual(a, b) {
		t.Fatal("different reminders differ")
	}
	b.ReminderMinutes = nil
	if detailEqual(a, b) {
		t.Fatal("a missing reminder differs")
	}
}

func TestDetailEqualComparesBodyTypeAndCategories(t *testing.T) {
	a, b := thin(t, t1), thin(t, t1)
	b.BodyType = "text"
	if detailEqual(a, b) {
		t.Fatal("different body types differ")
	}
	b = thin(t, t1)
	b.CategoriesJSON = `["Fixture"]`
	if detailEqual(a, b) {
		t.Fatal("different categories differ")
	}
}

// mustCapture is Capture for copies that are valid.
func mustCapture(t testing.TB, old *Event, in Event) Event {
	t.Helper()
	got, err := Capture(old, in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

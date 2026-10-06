package calendar

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	acctTeams   = "tenant-1/user-1"
	acctOutlook = "outlook-1"
)

// allDetail is every detail name; a row that lists it knows none of the detail group.
var allDetail = []Field{
	FieldJoinURL, FieldShortJoinURL, FieldDialIn, FieldMeetingChatID, FieldAttendees, FieldRooms, FieldBody,
	FieldBodyPreview, FieldAttachments, FieldCategories, FieldRecurrence, FieldReminder,
}

// row is a row of one source for the merge tests: invented data on 2026-11-02, one subject, 13:00
// to 14:00 UTC, last modified at lm (a time of day on that date), every flag unknown and every
// text field known. mods adjust it.
func row(t testing.TB, src Source, lm string, mods ...func(*Event)) Event {
	acct := acctTeams
	if src == SourceOutlook {
		acct = acctOutlook
	}
	e := Event{
		Source: src, AccountID: acct, SourceID: string(src) + "-1", GlobalID: "uid-1", ICalUID: "uid-1",
		Subject: "Fixture Weekly Sync", Start: mustTime(t, "2026-11-02T13:00:00Z"), End: mustTime(t, "2026-11-02T14:00:00Z"),
	}
	if lm != "" {
		e.LastModified = tp(t, "2026-11-02T"+lm+"Z")
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func unknownOf(fields ...Field) func(*Event) {
	return func(e *Event) { e.Unknown = NormalizeUnknown(fields) }
}

func flags(all, org, priv, canc Tri) func(*Event) {
	return func(e *Event) { e.AllDay, e.IsOrganizer, e.IsPrivate, e.Cancelled = all, org, priv, canc }
}

func detailAsOf(t testing.TB, lm string) func(*Event) {
	return func(e *Event) { e.DetailAsOf = tp(t, "2026-11-02T"+lm+"Z") }
}

func freshAt(t testing.TB, teams, outlook string) map[Source]time.Time {
	return map[Source]time.Time{
		SourceTeams:   mustTime(t, "2026-11-02T"+teams+"Z"),
		SourceOutlook: mustTime(t, "2026-11-02T"+outlook+"Z"),
	}
}

func fillOf(m Merged, f Field) *Fill {
	for i := range m.Filled {
		if m.Filled[i].Field == f {
			return &m.Filled[i]
		}
	}
	return nil
}

func TestMergeEmptyAndSingle(t *testing.T) {
	if got := Merge(nil, nil); !reflect.DeepEqual(got, Merged{}) {
		t.Fatalf("got %+v", got)
	}
	e := row(t, SourceTeams, "09:00:00", flags(TriFalse, TriFalse, TriFalse, TriFalse))
	got := Merge([]Event{e}, nil)
	if got.Subject != e.Subject || got.Removed || len(got.Filled) != 0 || !reflect.DeepEqual(got.Sources, []Source{SourceTeams}) {
		t.Fatalf("got %+v", got)
	}
}

// Example A: a twin whose last-modified times are two seconds apart.
func TestMergeExampleA(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", flags(TriFalse, TriFalse, TriFalse, TriFalse), detailAsOf(t, "09:00:00"), func(e *Event) {
		e.Location, e.IsOnlineMeeting, e.HasAttachments = "Fixture Room Alpha", TriTrue, TriFalse
		e.OnlineMeetingURL, e.AttendeesJSON = "https://teams.example.test/l/meetup-join/fixture", attendeesThree
	})
	outlook := row(t, SourceOutlook, "09:00:02", flags(TriFalse, TriFalse, TriFalse, TriFalse),
		unknownOf(append([]Field{FieldLocation}, allDetail...)...), func(e *Event) { e.Location = "" })
	fresh := freshAt(t, "09:10:00", "09:30:00")
	for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
		m := Merge(rows, fresh)
		if m.Source != SourceOutlook || m.SourceID != "outlook-1" {
			t.Fatalf("the fresher cache is the schedule base: %s %s", m.Source, m.SourceID)
		}
		if m.Location != "Fixture Room Alpha" || !m.IsOnlineMeeting.Is(true) {
			t.Fatalf("location %q online %v", m.Location, m.IsOnlineMeeting)
		}
		if f := fillOf(m, FieldLocation); f == nil || f.Source != SourceTeams || !f.AsOf.Equal(mustTime(t, "2026-11-02T09:00:00Z")) {
			t.Fatalf("location fill: %+v", m.Filled)
		}
		if fillOf(m, FieldIsOnlineMeeting) == nil {
			t.Fatalf("online fill: %+v", m.Filled)
		}
		if !m.DetailAsOf.Equal(mustTime(t, "2026-11-02T09:00:00Z")) || DetailLevel(m.Event) != DetailFull {
			t.Fatalf("detail %v level %s", m.DetailAsOf, DetailLevel(m.Event))
		}
		if m.AttendeesJSON != attendeesThree || m.OnlineMeetingURL == "" {
			t.Fatalf("detail not taken from the detail base: %+v", m.Event)
		}
		if !reflect.DeepEqual(m.Sources, []Source{SourceTeams, SourceOutlook}) {
			t.Fatalf("sources %v", m.Sources)
		}
		if got := UnknownFields(m.Event); len(got) != 0 {
			t.Fatalf("unknown fields %v", got)
		}
	}
}

// Example B: Outlook saw a later move.
func TestMergeExampleB(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", flags(TriFalse, TriFalse, TriFalse, TriFalse), detailAsOf(t, "09:00:00"), func(e *Event) {
		e.Location, e.AttendeesJSON = "Fixture Room Alpha", attendeesThree
		e.Start, e.End = mustTime(t, "2026-11-02T13:00:00Z"), mustTime(t, "2026-11-02T14:00:00Z")
	})
	outlook := row(t, SourceOutlook, "10:30:00", flags(TriFalse, TriFalse, TriFalse, TriFalse),
		unknownOf(append([]Field{FieldLocation}, allDetail...)...), func(e *Event) {
			e.Start, e.End = mustTime(t, "2026-11-02T14:00:00Z"), mustTime(t, "2026-11-02T15:00:00Z")
		})
	for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
		m := Merge(rows, nil)
		if m.Source != SourceOutlook || !m.Start.Equal(mustTime(t, "2026-11-02T14:00:00Z")) {
			t.Fatalf("base %s start %v", m.Source, m.Start)
		}
		if f := fillOf(m, FieldLocation); f == nil || !f.AsOf.Equal(mustTime(t, "2026-11-02T09:00:00Z")) || m.Location != "Fixture Room Alpha" {
			t.Fatalf("location %q fills %+v", m.Location, m.Filled)
		}
		if m.AttendeesJSON != attendeesThree || DetailLevel(m.Event) != DetailStale {
			t.Fatalf("level %s attendees %q", DetailLevel(m.Event), m.AttendeesJSON)
		}
	}
}

// Example C: an all-day event whose flag Outlook has not located.
func TestMergeExampleC(t *testing.T) {
	teams := row(t, SourceTeams, "12:00:00", func(e *Event) {
		e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
	})
	outlook := row(t, SourceOutlook, "12:00:30", func(e *Event) {
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
		m := Merge(rows, nil)
		if m.Source != SourceOutlook {
			t.Fatalf("base %s", m.Source)
		}
		if !m.AllDay.Is(true) || m.StartDate != "2026-11-03" || m.EndDate != "2026-11-04" || !m.Start.Equal(teams.Start) || !m.End.Equal(teams.End) {
			t.Fatalf("the whole block must come from teams: %+v", m.Event)
		}
		if fillOf(m, FieldAllDay) == nil {
			t.Fatalf("fills %+v", m.Filled)
		}
	}
	// Moved to a timed slot: nothing is filled and Outlook's times stand.
	moved := outlook
	moved.Start, moved.End = mustTime(t, "2026-11-05T09:00:00Z"), mustTime(t, "2026-11-05T10:00:00Z")
	m := Merge([]Event{teams, moved}, nil)
	if m.AllDay.Known() || !m.Start.Equal(moved.Start) || m.StartDate != "" || fillOf(m, FieldAllDay) != nil {
		t.Fatalf("a timed slot must stand: %+v", m.Event)
	}
	if got := UnknownFields(m.Event); !contains(got, "all_day") {
		t.Fatalf("all_day must stay unknown: %v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Example D: unknown flags in a tie.
func TestMergeExampleD(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", func(e *Event) { e.IsPrivate = TriFalse })
	outlook := row(t, SourceOutlook, "09:00:00")
	fresh := freshAt(t, "09:10:00", "09:30:00")
	for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
		m := Merge(rows, fresh)
		if m.Source != SourceOutlook || !m.IsPrivate.Is(false) || fillOf(m, FieldIsPrivate) == nil {
			t.Fatalf("is_private must be filled false: %+v %+v", m.IsPrivate, m.Filled)
		}
	}
	outlook.IsPrivate = TriTrue
	if m := Merge([]Event{teams, outlook}, fresh); !m.IsPrivate.Is(true) || fillOf(m, FieldIsPrivate) != nil {
		t.Fatalf("the base's known value wins: %v", m.IsPrivate)
	}
}

// Example E: the boundary of the skew.
func TestMergeExampleE(t *testing.T) {
	fresh := freshAt(t, "10:00:00", "09:00:00") // teams is the fresher cache
	teams := row(t, SourceTeams, "09:00:00", func(e *Event) { e.Subject = "teams" })
	for _, tt := range []struct {
		outlookLM string
		want      Source
	}{
		{"09:00:05", SourceTeams},             // difference equals the skew: a tie, freshness decides
		{"09:00:05.000000001", SourceOutlook}, // 1 ns more: the later wins outright
		{"08:59:55", SourceTeams},             // earlier by exactly the skew: a tie
		{"08:59:54.999999999", SourceTeams},   // earlier by more: teams outranks outright
	} {
		outlook := row(t, SourceOutlook, tt.outlookLM, func(e *Event) { e.Subject = "outlook" })
		for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
			if got := Merge(rows, fresh); got.Source != tt.want {
				t.Errorf("outlook at %s: base %s, want %s", tt.outlookLM, got.Source, tt.want)
			}
		}
	}
}

func TestMergeSkewBoundary(t *testing.T) {
	fresh := freshAt(t, "09:00:00", "10:00:00") // outlook is the fresher cache
	teams := row(t, SourceTeams, "09:00:05", func(e *Event) { e.Subject = "teams" })
	at5 := row(t, SourceOutlook, "09:00:00", func(e *Event) { e.Subject = "outlook" })
	if got := Merge([]Event{teams, at5}, fresh); got.Source != SourceOutlook {
		t.Fatalf("exactly 5 s is a tie, freshness decides: %s", got.Source)
	}
	over := row(t, SourceOutlook, "08:59:59.999999999", func(e *Event) { e.Subject = "outlook" })
	if got := Merge([]Event{teams, over}, fresh); got.Source != SourceTeams {
		t.Fatalf("5 s and 1 ns is decided by time: %s", got.Source)
	}
}

func TestMergeTieGoesToFresherCacheThenOutlook(t *testing.T) {
	lm := "09:00:00"
	outlook := row(t, SourceOutlook, lm, func(e *Event) { e.Subject = "outlook" })
	teams := row(t, SourceTeams, lm, func(e *Event) { e.Subject = "teams" })
	for _, rows := range [][]Event{{outlook, teams}, {teams, outlook}} {
		if got := Merge(rows, freshAt(t, "10:00:00", "09:00:00")); got.Source != SourceTeams {
			t.Fatalf("tie must follow cache freshness, got %s", got.Source)
		}
		if got := Merge(rows, freshAt(t, "09:00:00", "09:00:00")); got.Source != SourceOutlook {
			t.Fatalf("equal freshness must fall to outlook, got %s", got.Source)
		}
		if got := Merge(rows, nil); got.Source != SourceOutlook {
			t.Fatalf("no freshness must fall to outlook, got %s", got.Source)
		}
	}
	// A missing last-modified is a tie too.
	noTime := row(t, SourceOutlook, "", func(e *Event) { e.Subject = "outlook" })
	if got := Merge([]Event{noTime, teams}, freshAt(t, "10:00:00", "09:00:00")); got.Source != SourceTeams {
		t.Fatalf("a missing time must fall to freshness, got %s", got.Source)
	}
	if got := Merge([]Event{teams, noTime}, freshAt(t, "09:00:00", "10:00:00")); got.Source != SourceOutlook || got.LastModified == nil {
		t.Fatalf("the merged event takes a time from the other row when the base has none: %+v", got.LastModified)
	}
	// Two rows of one source keep input order.
	a, b := row(t, SourceTeams, lm, func(e *Event) { e.Subject = "a" }), row(t, SourceTeams, lm, func(e *Event) { e.Subject = "b" })
	if got := Merge([]Event{a, b}, nil); got.Subject != "a" {
		t.Fatalf("got %q", got.Subject)
	}
}

func TestMergeUnknownNeverOverridesKnown(t *testing.T) {
	// The base does not know its subject, organizer or flags; the other row does.
	teams := row(t, SourceTeams, "09:00:00", flags(TriTrue, TriTrue, TriFalse, TriFalse), func(e *Event) {
		e.Subject, e.Organizer, e.Location = "Teams Subject", "Alex Fixture", "Fixture Room Alpha"
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
		e.StartDate, e.EndDate = "2026-11-03", "2026-11-04"
	})
	outlook := row(t, SourceOutlook, "09:10:00", unknownOf(FieldSubject, FieldOrganizer, FieldLocation), func(e *Event) { e.Subject = "" })
	m := Merge([]Event{teams, outlook}, nil)
	if m.Source != SourceOutlook || m.Subject != "Teams Subject" || m.Organizer != "Alex Fixture" || m.Location != "Fixture Room Alpha" {
		t.Fatalf("unknown overrode known: %+v", m.Event)
	}
	// A known empty is not unknown: the base's empty location stands.
	known := row(t, SourceOutlook, "09:10:00")
	if m := Merge([]Event{teams, known}, nil); m.Location != "" || m.Subject != "Fixture Weekly Sync" {
		t.Fatalf("a known empty must stand: %+v", m.Event)
	}
	// Unknown in both stays unknown.
	both := row(t, SourceTeams, "09:00:00", unknownOf(FieldLocation))
	m = Merge([]Event{both, outlook}, nil)
	if !m.unknown(FieldLocation) || m.Location != "" {
		t.Fatalf("unknown in both rows must stay unknown: %v", m.Unknown)
	}
}

func TestMergeFillsUnknownScheduleFieldAndRecordsFill(t *testing.T) {
	var teams, outlook Event
	teams = row(t, SourceTeams, "09:00:00", func(e *Event) {
		e.TimeZone, e.TimeZoneIANA, e.UTCOffset, e.OrganizerAddress = "Pacific Standard Time", "America/Los_Angeles", "-08:00", "alex@example.test"
		e.Response, e.ShowAs, e.Organizer = "accepted", "busy", "Alex Fixture"
	})
	outlook = row(t, SourceOutlook, "09:00:00", unknownOf(FieldTimeZone, FieldTimeZoneIANA, FieldUTCOffset, FieldOrganizerAddress, FieldResponse, FieldShowAs, FieldOrganizer))
	m := Merge([]Event{outlook, teams}, freshAt(t, "08:00:00", "09:00:00"))
	if m.Source != SourceOutlook {
		t.Fatalf("base %s", m.Source)
	}
	for _, f := range []Field{FieldTimeZone, FieldTimeZoneIANA, FieldUTCOffset, FieldOrganizerAddress, FieldResponse, FieldShowAs, FieldOrganizer} {
		fl := fillOf(m, f)
		if fl == nil || fl.Source != SourceTeams || fl.AsOf == nil {
			t.Fatalf("no fill for %s: %+v", f, m.Filled)
		}
	}
	if m.TimeZone != "Pacific Standard Time" || m.TimeZoneIANA != "America/Los_Angeles" || m.UTCOffset != "-08:00" ||
		m.OrganizerAddress != "alex@example.test" || m.Response != "accepted" || m.ShowAs != "busy" || m.Organizer != "Alex Fixture" {
		t.Fatalf("not filled: %+v", m.Event)
	}
	if len(m.Unknown) != 0 {
		t.Fatalf("unknown after fill: %v", m.Unknown)
	}
	// With three rows, the second-best fills before the worst.
	third := row(t, SourceTeams, "08:00:00", func(e *Event) { e.TimeZone = "worst" })
	third.SourceID = "t2"
	if m := Merge([]Event{third, outlook, teams}, nil); m.TimeZone != "Pacific Standard Time" {
		t.Fatalf("fill order: %q", m.TimeZone)
	}
}

func TestMergeFillsIdentityFromOtherRow(t *testing.T) {
	a := row(t, SourceOutlook, "09:00:00", func(e *Event) { e.GlobalID, e.ICalUID, e.SeriesKey, e.EventType = "", "", "", "" })
	b := row(t, SourceTeams, "08:00:00", func(e *Event) {
		e.SeriesKey, e.EventType, e.OriginalStart = "series", EventOccurrence, tp(t, "2026-11-02T13:00:00Z")
	})
	m := Merge([]Event{a, b}, nil)
	if m.GlobalID != "uid-1" || m.ICalUID != "uid-1" || m.SeriesKey != "series" || m.EventType != EventOccurrence || m.OriginalStart == nil {
		t.Fatalf("%+v", m.Event)
	}
	if m.SourceID != "outlook-1" {
		t.Fatalf("SourceID belongs to the base: %q", m.SourceID)
	}
}

func TestMergeAllDayFromTeamsWhenOutlookUnknownAndLooksAllDay(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end string
		want       bool
	}{
		{"24 hours", "2026-11-03T00:00:00Z", "2026-11-04T00:00:00Z", true},
		{"23 hours", "2026-11-03T00:00:00Z", "2026-11-03T23:00:00Z", true},
		{"25 hours", "2026-11-03T00:00:00Z", "2026-11-04T01:00:00Z", true},
		{"two days", "2026-11-03T00:00:00Z", "2026-11-05T00:00:00Z", true},
		{"22 hours", "2026-11-03T00:00:00Z", "2026-11-03T22:00:00Z", false},
		{"26 hours", "2026-11-03T00:00:00Z", "2026-11-04T02:00:00Z", false},
		{"no end", "2026-11-03T00:00:00Z", "0001-01-01T00:00:00Z", false},
	} {
		teams := row(t, SourceTeams, "12:00:00", func(e *Event) {
			e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
			e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
		})
		outlook := row(t, SourceOutlook, "12:01:00", func(e *Event) { e.Start, e.End = mustTime(t, tc.start), mustTime(t, tc.end) })
		if got := Merge([]Event{teams, outlook}, nil).AllDay.Is(true); got != tc.want {
			t.Errorf("%s: all-day %v, want %v", tc.name, got, tc.want)
		}
	}
	// The start must be within 14 hours of the other row's.
	teams := row(t, SourceTeams, "12:00:00", func(e *Event) {
		e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
	})
	for start, want := range map[string]bool{"2026-11-02T18:00:00Z": true, "2026-11-02T17:59:00Z": false, "2026-11-03T22:00:00Z": true, "2026-11-03T22:01:00Z": false} {
		s := mustTime(t, start)
		outlook := row(t, SourceOutlook, "12:01:00", func(e *Event) { e.Start, e.End = s, s.Add(24*time.Hour) })
		if got := Merge([]Event{teams, outlook}, nil).AllDay.Is(true); got != want {
			t.Errorf("start %s: all-day %v, want %v", start, got, want)
		}
	}
}

func TestMergeAllDayNotFilledWhenOutlookMovedToTimedSlot(t *testing.T) {
	teams := row(t, SourceTeams, "12:00:00", func(e *Event) {
		e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
	})
	outlook := row(t, SourceOutlook, "12:01:00", func(e *Event) {
		e.Start, e.End = mustTime(t, "2026-11-05T09:00:00Z"), mustTime(t, "2026-11-05T10:00:00Z")
	})
	m := Merge([]Event{teams, outlook}, nil)
	if m.AllDay.Known() || m.StartDate != "" || !m.Start.Equal(outlook.Start) {
		t.Fatalf("%+v", m.Event)
	}
	// A known false from the other row is filled alone, whatever the base's instants.
	timedTeams := row(t, SourceTeams, "12:00:00", flags(TriFalse, TriUnknown, TriUnknown, TriUnknown))
	if m := Merge([]Event{timedTeams, outlook}, nil); !m.AllDay.Is(false) || fillOf(m, FieldAllDay) == nil || m.StartDate != "" {
		t.Fatalf("%+v", m.Event)
	}
	// A known flag on the base is never second-guessed.
	known := outlook
	known.AllDay = TriFalse
	if m := Merge([]Event{teams, known}, nil); !m.AllDay.Is(false) || !m.Start.Equal(known.Start) {
		t.Fatalf("%+v", m.Event)
	}
}

func TestMergeCancelledTrueFilledFalseNot(t *testing.T) {
	outlook := row(t, SourceOutlook, "10:00:00")
	canceled := row(t, SourceTeams, "09:00:00", func(e *Event) { e.Cancelled = TriTrue })
	live := row(t, SourceTeams, "09:00:00", func(e *Event) { e.Cancelled = TriFalse })
	for _, rows := range [][]Event{{canceled, outlook}, {outlook, canceled}} {
		if m := Merge(rows, nil); !m.Cancelled.Is(true) || fillOf(m, FieldCancelled) == nil {
			t.Fatalf("a true must be filled: %v", m.Cancelled)
		}
	}
	m := Merge([]Event{live, outlook}, nil)
	if m.Cancelled.Known() || fillOf(m, FieldCancelled) != nil {
		t.Fatalf("a false from an older row must not be filled: %v", m.Cancelled)
	}
	if got := UnknownFields(m.Event); !contains(got, "cancelled") {
		t.Fatalf("cancelled must stay unknown: %v", got)
	}
	// The base's known value wins in both directions.
	outlook.Cancelled = TriFalse
	if m := Merge([]Event{canceled, outlook}, nil); !m.Cancelled.Is(false) {
		t.Fatalf("the base's false must stand: %v", m.Cancelled)
	}
}

func TestMergeDetailBaseIsRowWithDetailAsOf(t *testing.T) {
	full := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) {
		e.AttendeesJSON, e.BodyText, e.BodyType, e.BodyPreview = attendeesTwo, "Agenda", "text", "Agenda"
		e.ReminderMinutes, e.DetailRawJSON, e.HasAttachments = intp(10), `{"r":1}`, TriTrue
	})
	empty := row(t, SourceOutlook, "09:30:00", func(e *Event) { e.CategoriesJSON = `["Fixture"]`; e.BodyPreview = "From outlook" })
	m := Merge([]Event{full, empty}, nil)
	if m.Source != SourceOutlook {
		t.Fatalf("the later row is the schedule base: %s", m.Source)
	}
	// The detail base is the row with a DetailAsOf, whatever the schedule base. An empty detail field
	// of it is filled from the other row.
	if m.AttendeesJSON != attendeesTwo || m.BodyText != "Agenda" || m.BodyType != "text" || m.BodyPreview != "Agenda" ||
		m.CategoriesJSON != `["Fixture"]` || m.ReminderMinutes == nil || *m.ReminderMinutes != 10 || m.DetailRawJSON != `{"r":1}` {
		t.Fatalf("%+v", m.Event)
	}
	if !m.DetailAsOf.Equal(*full.DetailAsOf) || m.HasAttachments != TriTrue {
		t.Fatalf("detail as of %v, has attachments %v", m.DetailAsOf, m.HasAttachments)
	}
	// With both set, the later DetailAsOf wins; equal or nil falls to the schedule base.
	later := row(t, SourceOutlook, "08:00:00", detailAsOf(t, "09:30:00"), func(e *Event) { e.AttendeesJSON = attendeesThree })
	if m := Merge([]Event{full, later}, nil); m.AttendeesJSON != attendeesThree || !m.DetailAsOf.Equal(*later.DetailAsOf) {
		t.Fatalf("%q %v", m.AttendeesJSON, m.DetailAsOf)
	}
	same := later
	same.DetailAsOf = full.DetailAsOf
	if m := Merge([]Event{full, same}, nil); m.AttendeesJSON != attendeesTwo { // teams has the later schedule time
		t.Fatalf("an equal detail time falls to the schedule base: %q", m.AttendeesJSON)
	}
	// An unknown reminder in the detail base is filled; a known "none" is a statement and stays.
	unknownReminder := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), unknownOf(FieldReminder))
	if m := Merge([]Event{unknownReminder, full}, nil); m.ReminderMinutes == nil {
		t.Fatal("an unknown reminder must be filled")
	}
	none := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"))
	if m := Merge([]Event{none, full}, nil); m.ReminderMinutes != nil {
		t.Fatal("a known no-reminder must stand")
	}
	// The first-seen and last-seen times span the rows.
	a, b := full, empty
	a.FirstSeenAt, a.SeenAt = mustTime(t, "2026-11-01T00:00:00Z"), mustTime(t, "2026-11-02T00:00:00Z")
	b.FirstSeenAt, b.SeenAt = mustTime(t, "2026-11-01T12:00:00Z"), mustTime(t, "2026-11-03T00:00:00Z")
	if m := Merge([]Event{a, b}, nil); !m.FirstSeenAt.Equal(a.FirstSeenAt) || !m.SeenAt.Equal(b.SeenAt) {
		t.Fatalf("%v %v", m.FirstSeenAt, m.SeenAt)
	}
}

func TestMergeJoinFieldsNotFilledWhenNotOnline(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) {
		e.IsOnlineMeeting, e.OnlineMeetingURL, e.TeamsThreadID, e.AttendeesJSON = TriTrue, "https://teams.example.test/l/meetup-join/x", "19:meeting_A@thread.v2", attendeesTwo
	})
	// Outlook is later by more than the skew and says it is not online: the join fields stay empty.
	outlook := row(t, SourceOutlook, "10:00:00", func(e *Event) { e.IsOnlineMeeting = TriFalse })
	m := Merge([]Event{teams, outlook}, nil)
	if m.OnlineMeetingURL != "" || m.TeamsThreadID != "" || m.AttendeesJSON != attendeesTwo || !m.IsOnlineMeeting.Is(false) {
		t.Fatalf("%+v", m.Event)
	}
	// Within the skew, or when the merged flag is not known false, they are filled.
	near := row(t, SourceOutlook, "09:00:03", func(e *Event) { e.IsOnlineMeeting = TriFalse })
	if m := Merge([]Event{teams, near}, nil); m.OnlineMeetingURL == "" {
		t.Fatal("within the skew the links are filled")
	}
	unknownOnline := row(t, SourceOutlook, "10:00:00")
	if m := Merge([]Event{teams, unknownOnline}, nil); m.OnlineMeetingURL == "" || !m.IsOnlineMeeting.Is(true) {
		t.Fatalf("an unknown flag must not block the links: %+v", m.Event)
	}
	// A schedule base that has its own links keeps them.
	outlook.TeamsThreadID = "19:meeting_B@thread.v2"
	if m := Merge([]Event{teams, outlook}, nil); m.TeamsThreadID != "19:meeting_B@thread.v2" || m.OnlineMeetingURL != "" {
		t.Fatalf("the schedule base's links stand alone: %+v", m.Event)
	}
}

func TestMergeMixedDetailReadsStale(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) { e.AttendeesJSON = attendeesTwo })
	outlook := row(t, SourceOutlook, "09:30:00")
	m := Merge([]Event{teams, outlook}, nil)
	if DetailLevel(m.Event) != DetailStale || m.AttendeesJSON != attendeesTwo {
		t.Fatalf("level %s", DetailLevel(m.Event))
	}
	// Rooms on a merged event carry the staleness.
	m.LocationsJSON = roomJSON
	for _, r := range Rooms(m.Event) {
		if r.Kind == RoomStructured && !r.Stale {
			t.Fatalf("a structured room must read stale: %+v", r)
		}
	}
}

func TestMergeDetailLevelSkew(t *testing.T) {
	for _, tc := range []struct {
		detail, mod string
		want        string
	}{
		{"09:00:00", "09:00:05", DetailFull},
		{"09:00:00", "09:00:05.000000001", DetailStale},
		{"09:00:05", "09:00:00", DetailFull},
	} {
		e := Event{DetailAsOf: tp(t, "2026-11-02T"+tc.detail+"Z"), LastModified: tp(t, "2026-11-02T"+tc.mod+"Z")}
		if got := DetailLevel(e); got != tc.want {
			t.Errorf("detail %s modified %s: %s, want %s", tc.detail, tc.mod, got, tc.want)
		}
	}
	if DetailLevel(Event{}) != DetailBasic || DetailLevel(Event{DetailAsOf: tp(t, "2026-11-02T09:00:00Z")}) != DetailFull {
		t.Fatal("basic without a detail time; full without a modification time")
	}
}

func TestMergeSourcesOrderFixed(t *testing.T) {
	teams, outlook := row(t, SourceTeams, "09:00:00"), row(t, SourceOutlook, "09:00:00")
	other := row(t, "other", "09:00:00")
	other2 := row(t, "zzz", "09:00:00")
	for _, rows := range [][]Event{{teams, outlook}, {outlook, teams}} {
		if got := Merge(rows, nil).Sources; !reflect.DeepEqual(got, []Source{SourceTeams, SourceOutlook}) {
			t.Fatalf("%v", got)
		}
	}
	if got := Merge([]Event{other2, outlook, other, teams}, nil).Sources; !reflect.DeepEqual(got, []Source{SourceTeams, SourceOutlook, "other", "zzz"}) {
		t.Fatalf("%v", got)
	}
}

func TestMergeUnknownIsIntersection(t *testing.T) {
	a := row(t, SourceTeams, "09:00:00", unknownOf(FieldLocation, FieldSubject, FieldAttendees), func(e *Event) { e.Subject = "" })
	b := row(t, SourceOutlook, "09:00:00", unknownOf(FieldLocation, FieldBody), func(e *Event) { e.Location = "" })
	m := Merge([]Event{a, b}, nil)
	if !reflect.DeepEqual(m.Unknown, []Field{FieldLocation}) {
		t.Fatalf("%v", m.Unknown)
	}
	// A flag unknown in both rows stays unknown, a flag known in either is known.
	a.IsPrivate = TriTrue
	if got := UnknownFields(Merge([]Event{a, b}, nil).Event); contains(got, "is_private") || !contains(got, "cancelled") {
		t.Fatalf("%v", got)
	}
}

func TestMergeFlagsFillAndAttachments(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", func(e *Event) { e.IsOrganizer, e.HasAttachments, e.IsOnlineMeeting = TriTrue, TriTrue, TriTrue })
	outlook := row(t, SourceOutlook, "09:00:01")
	m := Merge([]Event{teams, outlook}, freshAt(t, "08:00:00", "09:00:00"))
	if !m.IsOrganizer.Is(true) || !m.HasAttachments.Is(true) || !m.IsOnlineMeeting.Is(true) || fillOf(m, FieldIsOrganizer) == nil || fillOf(m, FieldHasAttachments) == nil {
		t.Fatalf("%+v %+v", m.Event, m.Filled)
	}
}

func TestMergedEmbedsEvent(t *testing.T) {
	// A Merged is usable wherever an Event is, and its Removed fields start empty for a live event.
	m := Merge([]Event{row(t, SourceTeams, "09:00:00")}, nil)
	if m.Removed || m.RemovedAt != nil || m.RemovedBy != nil || !strings.Contains(m.Subject, "Fixture") {
		t.Fatalf("%+v", m)
	}
}

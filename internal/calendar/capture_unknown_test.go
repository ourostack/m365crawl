package calendar

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestCaptureUnknownFlagKeepsOld(t *testing.T) {
	first := thin(t, t1)
	first.IsPrivate, first.IsOrganizer = TriTrue, TriTrue
	old := mustCapture(t, nil, first)
	later := thin(t, t3)
	later.IsPrivate, later.IsOrganizer = TriUnknown, TriFalse
	got := mustCapture(t, &old, later)
	if got.IsPrivate != TriTrue || got.IsOrganizer != TriFalse {
		t.Fatalf("an unknown flag must keep the old value and a known one replace it: %v %v", got.IsPrivate, got.IsOrganizer)
	}
	// A stale copy never replaces, but it fills what nothing has stated.
	cur := thin(t, t3)
	cur.IsPrivate = TriUnknown
	stored := mustCapture(t, nil, cur)
	staleKnown := thin(t, t1)
	staleKnown.IsPrivate, staleKnown.IsOrganizer = TriTrue, TriTrue
	got = mustCapture(t, &stored, staleKnown)
	if got.IsPrivate != TriTrue || got.IsOrganizer != TriFalse {
		t.Fatalf("a stale copy fills only the unknown flag: %v %v", got.IsPrivate, got.IsOrganizer)
	}
	// The result does not depend on the order.
	converge(t, "flag", []Event{first, later, staleKnown})
}

func TestCaptureUnknownLocationDoesNotClear(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	unknownLoc := thin(t, t3)
	unknownLoc.Location = ""
	unknownLoc.Unknown = append(unknownLoc.Unknown, FieldLocation)
	got := mustCapture(t, &old, unknownLoc)
	if got.Location != "Fixture Room Alpha" || got.unknown(FieldLocation) {
		t.Fatalf("an unknown location must not clear: %q %v", got.Location, got.Unknown)
	}
	// Unknown on a first sighting stays unknown, and a later known copy fills it even if older.
	first := mustCapture(t, nil, unknownLoc)
	if !first.unknown(FieldLocation) {
		t.Fatal("a first copy that does not know the location stores it unknown")
	}
	older := thin(t, t1)
	if got := mustCapture(t, &first, older); got.Location != "Fixture Room Alpha" || got.unknown(FieldLocation) {
		t.Fatalf("an older known copy fills an unknown location: %q %v", got.Location, got.Unknown)
	}
}

func TestCaptureKnownEmptyLocationNewerClears(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	cleared := thin(t, t3)
	cleared.Location = ""
	got := mustCapture(t, &old, cleared)
	if got.Location != "" || got.unknown(FieldLocation) {
		t.Fatalf("a known empty location from a newer copy clears: %q %v", got.Location, got.Unknown)
	}
	// An older known empty does not.
	cur := mustCapture(t, nil, rich(t, t3))
	if got := mustCapture(t, &cur, func() Event { e := thin(t, t1); e.Location = ""; return e }()); got.Location != "Fixture Room Alpha" {
		t.Fatalf("an older empty location must not clear: %q", got.Location)
	}
}

func TestCaptureAllDayBlockMovesTogether(t *testing.T) {
	first := thin(t, t1)
	setAllDay(&first, "2026-10-05", "2026-10-06")
	old := mustCapture(t, nil, first)
	// A newer copy that does not know the flag and still gives the same days keeps the block.
	same := thin(t, t3)
	same.AllDay = TriUnknown
	same.Start, same.End = mustTime(t, "2026-10-05T00:00:00Z"), mustTime(t, "2026-10-06T00:00:00Z")
	if got := mustCapture(t, &old, same); !got.AllDay.Is(true) || got.StartDate != "2026-10-05" || got.EndDate != "2026-10-06" {
		t.Fatalf("%+v", got)
	}
	// A newer copy that does not know the flag and moves the instants to other times takes them.
	// The old block would leave the event on its old days with new instants, so the flag turns
	// unknown and the dates clear: the event shows on its new day.
	unknown := thin(t, t3)
	unknown.AllDay, unknown.StartDate, unknown.EndDate = TriUnknown, "2026-12-01", "2026-12-02" // dates of an unknown flag are ignored
	unknown.Start, unknown.End = mustTime(t, "2026-10-06T16:00:00Z"), mustTime(t, "2026-10-06T17:00:00Z")
	got := mustCapture(t, &old, unknown)
	if got.AllDay.Known() || got.StartDate != "" || got.EndDate != "" || !got.Start.Equal(unknown.Start) {
		t.Fatalf("%+v", got)
	}
	// An all-day copy at a still-newer time that agrees with the block is not contradicted.
	back := thin(t, "2026-10-04T10:00:00Z")
	back.AllDay = TriUnknown
	back.Start, back.End = mustTime(t, "2026-10-05T00:00:00Z"), mustTime(t, "2026-10-06T00:00:00Z")
	if again := mustCapture(t, &got, back); !again.AllDay.Is(true) || again.StartDate != "2026-10-05" {
		t.Fatalf("a later copy that agrees with the stated block shows it again: %+v", again)
	}
	// A newer copy that knows it takes the whole block, including a known false with no dates.
	timedAgain := thin(t, t3)
	got = mustCapture(t, &old, timedAgain)
	if !got.AllDay.Is(false) || got.StartDate != "" || got.EndDate != "" {
		t.Fatalf("%+v", got)
	}
	moved := thin(t, t3)
	setAllDay(&moved, "2026-10-08", "2026-10-09")
	got = mustCapture(t, &old, moved)
	if got.StartDate != "2026-10-08" || got.EndDate != "2026-10-09" {
		t.Fatalf("%+v", got)
	}
	converge(t, "all day", []Event{first, unknown, timedAgain, moved})
}

func TestCaptureUnknownOnlineMeetingDoesNotClearJoinFields(t *testing.T) {
	old := mustCapture(t, nil, rich(t, t1))
	silent := thin(t, t3)
	silent.IsOnlineMeeting = TriUnknown
	got := mustCapture(t, &old, silent)
	if got.OnlineMeetingURL == "" || got.TeamsThreadID == "" || got.IsOnlineMeeting != TriTrue {
		t.Fatalf("an unknown flag cleared: %+v", got)
	}
	if _, has := ParseFieldClocks(got.FieldClocksJSON)[noLinks]; has {
		t.Fatal("an unknown flag must not move the no_links clock")
	}
	// Known false clears, and what is cleared is known to be none.
	off := thin(t, t3)
	off.IsOnlineMeeting = TriFalse
	got = mustCapture(t, &old, off)
	if got.OnlineMeetingURL != "" || got.unknown(FieldJoinURL) || got.unknown(FieldMeetingChatID) {
		t.Fatalf("%+v", got)
	}
}

func TestCaptureUnknownSetShrinksWhenFieldArrives(t *testing.T) {
	old := mustCapture(t, nil, thin(t, t1))
	for _, f := range []Field{FieldAttendees, FieldBody, FieldReminder, FieldOrganizerAddress} {
		if !old.unknown(f) {
			t.Fatalf("%s must start unknown: %v", f, old.Unknown)
		}
	}
	got := mustCapture(t, &old, rich(t, t2))
	if !reflect.DeepEqual(got.Unknown, []Field{FieldOrganizerAddress, FieldTimeZoneIANA, FieldUTCOffset}) {
		t.Fatalf("%v", got.Unknown)
	}
	// A field that had a value and now receives an unknown is still known, and the set never grows.
	back := mustCapture(t, &got, thin(t, t3))
	if !reflect.DeepEqual(back.Unknown, got.Unknown) || back.AttendeesJSON != attendeesTwo {
		t.Fatalf("%v", back.Unknown)
	}
	// A copy that says "no attendees" states nothing for the unit, so it cannot mark it known: only
	// a copy whose unit won can, and that keeps Unknown independent of arrival order.
	emptyAttendees := thin(t, t3)
	emptyAttendees.Unknown = without(emptyAttendees.Unknown, FieldAttendees)
	if got := mustCapture(t, &old, emptyAttendees); !got.unknown(FieldAttendees) || got.AttendeesJSON != "" {
		t.Fatalf("an empty list is not a statement: %v %q", got.Unknown, got.AttendeesJSON)
	}
	// A name listed unknown next to a value is read as known: the value wins.
	contradictory := rich(t, t2)
	contradictory.Unknown = []Field{FieldAttendees, FieldSubject}
	got = mustCapture(t, nil, contradictory)
	if got.unknown(FieldAttendees) || got.AttendeesJSON != attendeesTwo || got.unknown(FieldSubject) {
		t.Fatalf("%v", got.Unknown)
	}
}

func TestCaptureHasAttachmentsPeersTrueWins(t *testing.T) {
	for _, tc := range []struct{ a, b, want Tri }{
		{TriTrue, TriFalse, TriTrue}, {TriFalse, TriTrue, TriTrue},
		{TriUnknown, TriFalse, TriFalse}, {TriFalse, TriUnknown, TriFalse},
		{TriUnknown, TriUnknown, TriUnknown}, {TriUnknown, TriTrue, TriTrue},
	} {
		a, b := thin(t, t1), thin(t, t1)
		a.HasAttachments, b.HasAttachments = tc.a, tc.b
		a.LastModified, b.LastModified = nil, nil // peers: no times to order them
		if got := converge(t, "attachments", []Event{a, b}); got.HasAttachments != tc.want {
			t.Errorf("%v %v: %v, want %v", tc.a, tc.b, got.HasAttachments, tc.want)
		}
		// And with times, it stays sticky.
		a.LastModified, b.LastModified = tp(t, t1), tp(t, t3)
		if got := converge(t, "attachments", []Event{a, b}); got.HasAttachments != tc.want {
			t.Errorf("timed %v %v: %v, want %v", tc.a, tc.b, got.HasAttachments, tc.want)
		}
	}
}

func TestCaptureScheduleClocksStayCompact(t *testing.T) {
	// A row whose copies state everything stores no schedule clock; a field a newer copy does not
	// know keeps its own, older, clock, and a later arrival is judged against it.
	first := thin(t, t1)
	first.Subject = "First"
	old := mustCapture(t, nil, first)
	if len(ParseFieldClocks(old.FieldClocksJSON)) != 1 { // location only
		t.Fatalf("%s", old.FieldClocksJSON)
	}
	unknown := thin(t, t3)
	unknown.Subject = ""
	unknown.Unknown = append(unknown.Unknown, FieldSubject)
	got := mustCapture(t, &old, unknown)
	clocks := ParseFieldClocks(got.FieldClocksJSON)
	if !clocks["subject"].Equal(mustTime(t, t1)) || got.Subject != "First" {
		t.Fatalf("subject keeps its own clock: %v %q", clocks, got.Subject)
	}
	between := thin(t, t2)
	between.Subject = "Between"
	if got := mustCapture(t, &got, between); got.Subject != "Between" {
		t.Fatalf("a copy newer than the subject's clock replaces it: %q", got.Subject)
	}
	converge(t, "clocks", []Event{first, unknown, between})
}

func TestCaptureUnknownLinksStatusFollowsTheTakenCopy(t *testing.T) {
	a := thin(t, t1)
	a.OnlineMeetingURL = "https://teams.example.test/a"
	b := thin(t, t3)
	b.OnlineMeetingURL, b.TeamsThreadID = "https://teams.example.test/b", "19:meeting_B@thread.v2"
	got := converge(t, "links", []Event{a, b})
	if got.unknown(FieldJoinURL) || got.unknown(FieldMeetingChatID) || !got.unknown(FieldDialIn) {
		t.Fatalf("%v", got.Unknown)
	}
	// An older copy that states the links does not change what the newer one said is unknown.
	newer := thin(t, t3)
	newer.OnlineMeetingURL = "https://teams.example.test/b"
	older := thin(t, t1)
	older.OnlineMeetingURL, older.TeamsThreadID = "https://teams.example.test/a", "19:meeting_A@thread.v2"
	got = converge(t, "older links", []Event{newer, older})
	if !got.unknown(FieldMeetingChatID) || got.TeamsThreadID != "" {
		t.Fatalf("%v %q", got.Unknown, got.TeamsThreadID)
	}
}

// randomUnknownCopy is a copy with random unknowns in every group, as two sources produce them.
func randomUnknownCopy(t *testing.T, r *rand.Rand, src Source) Event {
	e := randomCopy(t, r)
	if src == SourceOutlook {
		e.Source, e.AccountID, e.SourceID = SourceOutlook, "outlook-1", "o1"
	}
	return e
}

// The merge of two sources' rows, whichever order they were captured and merged in, is one event.
func TestMergeOfTwoSourcesDoesNotDependOnOrder(t *testing.T) {
	r := rand.New(rand.NewSource(20261006)) //nolint:gosec // a fixed seed makes the sweep reproducible, not secret
	fresh := freshAt(t, "09:10:00", "09:30:00")
	for n := 0; n < sweepSize(600); n++ {
		var rows [2][]Event
		for s, src := range []Source{SourceTeams, SourceOutlook} {
			for i := 0; i < 1+r.Intn(3); i++ {
				rows[s] = append(rows[s], randomUnknownCopy(t, r, src))
			}
		}
		stored := func(copies []Event, order []int) Event {
			var e *Event
			for _, i := range order {
				next := mustCapture(t, e, copies[i])
				e = &next
			}
			return *e
		}
		var first *Merged
		for _, oa := range permutations(len(rows[0])) {
			for _, ob := range permutations(len(rows[1])) {
				a, b := stored(rows[0], oa), stored(rows[1], ob)
				for _, pair := range [][]Event{{a, b}, {b, a}} {
					m := Merge(pair, fresh)
					if first == nil {
						first = &m
					} else if !reflect.DeepEqual(*first, m) {
						t.Fatalf("set %d: arrival orders %v %v, merge order %s first: merged rows differ\n%+v\n%+v", n, oa, ob, pair[0].Source, *first, m)
					}
				}
			}
		}
	}
}

func TestCaptureSweepWithUnknownsIsReproducible(t *testing.T) {
	// The seeded generator must produce the unknowns the sweeps rely on.
	r := rand.New(rand.NewSource(1)) //nolint:gosec // a fixed seed makes the sweep reproducible, not secret
	var sawUnknownFlag, sawUnknownText, sawKnownAllDay bool
	for i := 0; i < 200; i++ {
		e := randomCopy(t, r)
		sawUnknownFlag = sawUnknownFlag || !e.Cancelled.Known()
		sawUnknownText = sawUnknownText || e.unknown(FieldSubject)
		sawKnownAllDay = sawKnownAllDay || e.AllDay.Is(true)
	}
	if !sawUnknownFlag || !sawUnknownText || !sawKnownAllDay {
		t.Fatal(fmt.Sprint("generator coverage: ", sawUnknownFlag, sawUnknownText, sawKnownAllDay))
	}
}

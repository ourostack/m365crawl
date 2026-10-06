package calendar

import (
	"encoding/json"
	"testing"
)

const (
	teamsList = `[{"name":"Pat Example","address":"Pat@Example.invalid","type":"Organizer","role":"Chair","response":"organizer"},` +
		`{"name":"Sam Example","address":"sam@example.invalid","type":"Optional","role":"Attendee","response":"none"},` +
		`{"name":"Jordan Fixture","address":"jordan@example.invalid","type":"Required","role":"Attendee","response":"tentative"},` +
		`{"name":"No Address","address":"","type":"Required","role":"Attendee","response":"none"}]`
	outlookList = `[{"name":"Sam Example","address":"SAM@example.invalid","type":"optional","response":"accepted"},` +
		`{"name":"no address","address":"","type":"required","response":"declined"},` +
		`{"name":"Late Addition","address":"late@example.invalid","type":"required","response":"none"}]`
)

func attendeesOf(t *testing.T, e Event) map[string]listAttendee {
	t.Helper()
	var list []listAttendee
	if err := json.Unmarshal([]byte(e.AttendeesJSON), &list); err != nil {
		t.Fatalf("attendees %q: %v", e.AttendeesJSON, err)
	}
	out := map[string]listAttendee{}
	for _, a := range list {
		out[attendeeKey(a)] = a
	}
	if len(out) != len(list) {
		t.Fatalf("duplicate attendee in %q", e.AttendeesJSON)
	}
	return out
}

func listRows(t *testing.T, teamsLM, outlookLM string) (Event, Event) {
	tm := row(t, SourceTeams, teamsLM, func(e *Event) { e.AttendeesJSON = teamsList }, detailAsOf(t, teamsLM))
	ol := row(t, SourceOutlook, outlookLM, func(e *Event) { e.AttendeesJSON = outlookList }, detailAsOf(t, outlookLM))
	return tm, ol
}

func TestMergeUnionsAttendeesAndTheNewerCopyAnswers(t *testing.T) {
	// Outlook is newer, as in 146 of 148 real twins, but holds the shorter list.
	tm, ol := listRows(t, "09:00:00", "11:00:00")
	m := Merge([]Event{tm, ol}, freshAt(t, "12:00:00", "12:00:00"))
	got := attendeesOf(t, m.Event)
	if len(got) != 5 {
		t.Fatalf("want the 5 distinct attendees, got %d: %s", len(got), m.AttendeesJSON)
	}
	if a := got["sam@example.invalid"]; a.Response != "accepted" || a.Type != "optional" {
		t.Errorf("a newer Outlook answer must override Teams for a shared attendee, got %+v", a)
	}
	if a := got["jordan@example.invalid"]; a.Response != "tentative" || a.Role != "Attendee" {
		t.Errorf("an attendee only Teams holds is kept, got %+v", a)
	}
	if a := got["pat@example.invalid"]; a.Role != "Chair" {
		t.Errorf("the address match ignores case, got %+v", a)
	}
	if _, ok := got["late@example.invalid"]; !ok {
		t.Error("an attendee only Outlook holds is kept")
	}
	if a := got["\x00no address"]; a.Response != "declined" {
		t.Errorf("an attendee without an address matches by name, got %+v", a)
	}
	var filled bool
	for _, f := range m.Filled {
		if f.Field == FieldAttendees {
			filled = true
		}
	}
	if !filled {
		t.Errorf("the union enlarged the Outlook-detail list, so attendees must be a filled field: %+v", m.Filled)
	}
}

func TestMergeAttendeesNewerTeamsKeepsItsAnswers(t *testing.T) {
	tm, ol := listRows(t, "11:00:00", "09:00:00")
	m := Merge([]Event{tm, ol}, freshAt(t, "12:00:00", "12:00:00"))
	got := attendeesOf(t, m.Event)
	if len(got) != 5 || got["sam@example.invalid"].Response != "none" || got["\x00no address"].Response != "none" {
		t.Errorf("a newer Teams answer wins: %s", m.AttendeesJSON)
	}
}

func TestMergeAttendeesNoFillWhenTheChosenListAlreadyHoldsEverything(t *testing.T) {
	sub := `[{"name":"Sam Example","address":"sam@example.invalid","type":"optional","response":"accepted"}]`
	tm := row(t, SourceTeams, "11:00:00", func(e *Event) { e.AttendeesJSON = teamsList }, detailAsOf(t, "11:00:00"))
	ol := row(t, SourceOutlook, "09:00:00", func(e *Event) { e.AttendeesJSON = sub }, detailAsOf(t, "09:00:00"))
	m := Merge([]Event{tm, ol}, freshAt(t, "12:00:00", "12:00:00"))
	for _, f := range m.Filled {
		if f.Field == FieldAttendees {
			t.Errorf("nothing was added, so no fill: %+v", m.Filled)
		}
	}
	if n := listLen(m.AttendeesJSON); n != 4 {
		t.Errorf("want Teams' 4, got %d", n)
	}
}

func TestMergeAttendeesFromOneCopyAreLeftAlone(t *testing.T) {
	tm := row(t, SourceTeams, "09:00:00", func(e *Event) { e.AttendeesJSON = teamsList }, detailAsOf(t, "09:00:00"))
	ol := row(t, SourceOutlook, "11:00:00")
	if m := Merge([]Event{tm, ol}, freshAt(t, "12:00:00", "12:00:00")); m.AttendeesJSON != teamsList {
		t.Errorf("a list only one copy holds is kept verbatim: %s", m.AttendeesJSON)
	}
	if m := Merge([]Event{tm}, nil); m.AttendeesJSON != teamsList {
		t.Errorf("a single row is untouched: %s", m.AttendeesJSON)
	}
	bad := row(t, SourceOutlook, "11:00:00", func(e *Event) { e.AttendeesJSON = "not json" })
	if m := Merge([]Event{tm, bad}, freshAt(t, "12:00:00", "12:00:00")); m.AttendeesJSON == "" {
		t.Error("an unreadable list is ignored, not allowed to clear the other")
	}
}

func TestMergeUnionsStructuredRooms(t *testing.T) {
	teamsRooms := `[{"name":"Fixture Room Alpha","kind":"ConferenceRoom","address":"1 Fixture Way","latitude":12.5,"longitude":65.5},{"name":"Fixture Room Beta","kind":"","address":""}]`
	outlookRooms := `[{"name":"fixture room alpha","kind":"","address":""},{"name":"","kind":"","address":"2 Fixture Way"}]`
	tm := row(t, SourceTeams, "09:00:00", func(e *Event) { e.LocationsJSON = teamsRooms }, detailAsOf(t, "09:00:00"))
	ol := row(t, SourceOutlook, "11:00:00", func(e *Event) { e.LocationsJSON = outlookRooms }, detailAsOf(t, "11:00:00"))
	m := Merge([]Event{tm, ol}, freshAt(t, "12:00:00", "12:00:00"))
	var got []listRoom
	if err := json.Unmarshal([]byte(m.LocationsJSON), &got); err != nil || len(got) != 3 {
		t.Fatalf("want 3 rooms, got %q (%v)", m.LocationsJSON, err)
	}
	if got[0].Name != "fixture room alpha" || got[0].Latitude != nil {
		t.Errorf("a shared room takes the newer copy's values: %+v", got[0])
	}
	var filled bool
	for _, f := range m.Filled {
		filled = filled || f.Field == FieldRooms
	}
	if !filled {
		t.Errorf("rooms must be a filled field: %+v", m.Filled)
	}
}

func TestListClockFallsBackToDetailTimeThenLastModified(t *testing.T) {
	clock := listClock("attendees")
	lm := row(t, SourceTeams, "08:00:00")
	if got := clock(lm); !got.Equal(*lm.LastModified) {
		t.Errorf("last modified: %v", got)
	}
	both := row(t, SourceTeams, "08:00:00", detailAsOf(t, "07:00:00"))
	if got := clock(both); !got.Equal(*both.DetailAsOf) {
		t.Errorf("detail time before last modified: %v", got)
	}
	stated := row(t, SourceTeams, "08:00:00", detailAsOf(t, "07:00:00"), func(e *Event) { e.FieldClocksJSON = `{"attendees":"2026-11-02T06:00:00.000Z"}` })
	if got := clock(stated); !got.Equal(mustTime(t, "2026-11-02T06:00:00Z")) {
		t.Errorf("the unit clock first: %v", got)
	}
	if got := clock(Event{}); !got.IsZero() {
		t.Errorf("no time at all: %v", got)
	}
}

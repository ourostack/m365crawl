package calendar

import "testing"

const (
	teamsJoin   = "https://teams.example.test/l/meetup-join/x"
	teamsChat   = "19:meeting_A@thread.v2"
	outlookJoin = "https://outlook.example.test/join/0005"
)

// teamsRich is a Teams copy that states the meeting links, two rooms and an organizer.
func teamsRich(t testing.TB) Event {
	return row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) {
		e.IsOnlineMeeting, e.OnlineMeetingURL, e.ShortJoinURL, e.TeamsThreadID = TriTrue, teamsJoin, "https://teams.example.test/meet/x", teamsChat
		e.DialInConferenceID, e.DialInTollNumber = "000000001", "+1 555 0100"
		e.Location, e.Organizer, e.OrganizerAddress, e.BodyPreview = "Fixture Room Alpha; Fixture Room Beta", "Pat Example", "pat@example.invalid", "teams preview"
		e.AttendeesJSON = attendeesTwo
	})
}

// outlookNewer is a newer, rich Outlook copy with its own link, location, organizer and no online flag.
func outlookNewer(t testing.TB) Event {
	return row(t, SourceOutlook, "10:00:00", detailAsOf(t, "10:00:00"), func(e *Event) {
		e.IsOnlineMeeting, e.OnlineMeetingURL = TriFalse, outlookJoin
		e.Location, e.Organizer, e.OrganizerAddress, e.BodyPreview = "Fixture Room 1", "Fixture Organizer", "fixture.organizer@example.test", "outlook preview"
		e.AttendeesJSON = attendeesTwo
	})
}

// Teams owns the Teams meeting: a newer Outlook copy that is also the detail base does not replace
// the join link, short link, dial-in or chat id, and the online flag stays true while the link is kept.
func TestMergeTeamsOwnsTheMeetingLinks(t *testing.T) {
	m := Merge([]Event{teamsRich(t), outlookNewer(t)}, nil)
	if m.Source != SourceOutlook && m.Subject == "" {
		t.Fatal("unexpected base")
	}
	if m.OnlineMeetingURL != teamsJoin || m.ShortJoinURL == "" || m.DialInConferenceID != "000000001" || m.DialInTollNumber != "+1 555 0100" || m.TeamsThreadID != teamsChat {
		t.Fatalf("the Teams links must stand: %+v", m.Event)
	}
	if !m.IsOnlineMeeting.Is(true) {
		t.Fatalf("is_online_meeting turned false with the join link kept: %v", m.IsOnlineMeeting)
	}
	// The schedule fields still follow the clock: Outlook is newer.
	if m.Organizer != "Fixture Organizer" {
		t.Fatalf("schedule fields keep the clock rule: %q", m.Organizer)
	}
	// Outlook fills the unit only when no Teams row states it.
	thin := row(t, SourceTeams, "09:00:00")
	m = Merge([]Event{thin, outlookNewer(t)}, nil)
	if m.OnlineMeetingURL != outlookJoin {
		t.Fatalf("Outlook fills a Teams gap: %q", m.OnlineMeetingURL)
	}
	// The unit is not mixed: Teams' stated unit replaces Outlook's whole, even when Teams lacks a part.
	partial := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) { e.OnlineMeetingURL, e.IsOnlineMeeting = teamsJoin, TriTrue })
	out := outlookNewer(t)
	out.TeamsThreadID = "19:meeting_OUTLOOK@thread.v2"
	if m = Merge([]Event{partial, out}, nil); m.TeamsThreadID != "" || m.OnlineMeetingURL != teamsJoin {
		t.Fatalf("one copy's version of the unit: %+v", m.Event)
	}
}

// The history rule stays: a later Outlook "not online" with no detail removes the links.
func TestMergeTeamsOwnerKeepsTheHistoryRule(t *testing.T) {
	teams := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) {
		e.IsOnlineMeeting, e.OnlineMeetingURL, e.TeamsThreadID = TriTrue, teamsJoin, teamsChat
	})
	thinOutlook := row(t, SourceOutlook, "10:00:00", func(e *Event) { e.IsOnlineMeeting = TriFalse })
	if m := Merge([]Event{teams, thinOutlook}, nil); m.OnlineMeetingURL != "" || m.TeamsThreadID != "" || !m.IsOnlineMeeting.Is(false) {
		t.Fatalf("%+v", m.Event)
	}
}

// Recordings attach by the meeting chat id, so the chat id is the Teams one whichever copy wins, in
// either input order and whichever cache is fresher.
func TestMergeChatIDIndependentOfTheWinner(t *testing.T) {
	for _, fresh := range []map[Source]string{{SourceTeams: "12:00:00", SourceOutlook: "08:00:00"}, {SourceTeams: "08:00:00", SourceOutlook: "12:00:00"}} {
		f := freshAt(t, fresh[SourceTeams], fresh[SourceOutlook])
		for _, rows := range [][]Event{{teamsRich(t), outlookNewer(t)}, {outlookNewer(t), teamsRich(t)}} {
			if m := Merge(rows, f); m.TeamsThreadID != teamsChat {
				t.Fatalf("chat id %q", m.TeamsThreadID)
			}
		}
	}
}

// Rooms are a union: the merged Location is the base's text, and MergedRooms still names a room only
// the other copy's text holds, once, with the structured and resource rooms.
func TestMergedRoomsAreAUnion(t *testing.T) {
	teams := teamsRich(t)
	teams.LocationsJSON = `[{"name":"Fixture Room Alpha","kind":"conferenceRoom"}]`
	out := outlookNewer(t)
	m := Merge([]Event{teams, out}, nil)
	if m.Location != "Fixture Room 1" {
		t.Fatalf("the location text is the base's: %q", m.Location)
	}
	var names []string
	for _, r := range MergedRooms(m) {
		names = append(names, r.Name)
	}
	want := []string{"Fixture Room 1", "Fixture Room Alpha", "Fixture Room Beta"}
	if len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("rooms %v, want %v", names, want)
	}
	// With the same text on both copies nothing is added or repeated.
	same := Merge([]Event{teams, row(t, SourceOutlook, "10:00:00", func(e *Event) { e.Location = "fixture room alpha" })}, nil)
	if got := MergedRooms(same); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// Overridden records a value one copy replaced, with the source that stands.
func TestMergeOverriddenFields(t *testing.T) {
	m := Merge([]Event{teamsRich(t), outlookNewer(t)}, nil)
	got := map[Field]string{}
	for _, o := range m.Overridden {
		got[o.Field] = o.From
	}
	for f, from := range map[Field]string{
		FieldOrganizer: "outlook", FieldLocation: "outlook", FieldBodyPreview: "outlook",
		FieldJoinURL: "teams", FieldMeetingChatID: "", FieldIsOnlineMeeting: "teams",
	} {
		if f == FieldMeetingChatID { // only Teams states a chat id: a fill or nothing, not an override
			if _, ok := got[f]; ok {
				t.Fatalf("a field one copy stated is not an override: %v", got)
			}
			continue
		}
		if got[f] != from {
			t.Fatalf("%s: from %q, want %q (%v)", f, got[f], from, got)
		}
	}
	if _, ok := got[FieldAttendees]; ok {
		t.Fatalf("equal attendee lists are no override: %v", got)
	}
	// Differing lists whose union is neither copy's say so.
	a := row(t, SourceTeams, "09:00:00", detailAsOf(t, "09:00:00"), func(e *Event) { e.AttendeesJSON = `[{"name":"A","address":"a@example.test"}]` })
	b := row(t, SourceOutlook, "10:00:00", detailAsOf(t, "10:00:00"), func(e *Event) { e.AttendeesJSON = `[{"name":"B","address":"b@example.test"}]` })
	found := false
	for _, o := range Merge([]Event{a, b}, nil).Overridden {
		if o.Field == FieldAttendees && o.From == OverrideUnion {
			found = true
		}
	}
	if !found {
		t.Fatal("a union list is recorded with from union")
	}
	if m := Merge([]Event{teamsRich(t)}, nil); len(m.Overridden) != 0 {
		t.Fatalf("one copy overrides nothing: %v", m.Overridden)
	}
}

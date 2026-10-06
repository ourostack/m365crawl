package calendar

import (
	"reflect"
	"testing"
)

func TestRoomsMergesTextStructuredResource(t *testing.T) {
	e := rich(t, t2)
	e.Location = "Fixture Room Alpha; Fixture Room Gamma ;https://teams.example.test/l/meetup-join/x; "
	e.LocationsJSON = `[{"name":"fixture room alpha","kind":"conferenceRoom","address":"alpha@example.test","latitude":47.5,"longitude":-122.25},
		{"name":"Fixture Room Delta","kind":"room"}]`
	e.AttendeesJSON = `[{"name":"Alex Fixture","address":"alex@example.test","type":"Required"},
		{"name":"Fixture Room Gamma","address":"gamma@example.test","type":"Resource"},
		{"name":"Fixture Room Epsilon","address":"epsilon@example.test","type":"resource"}]`
	got := Rooms(e)
	lat, lon := 47.5, -122.25
	want := []Room{
		{Name: "Fixture Room Alpha", Kind: "structured", LocationType: "conferenceRoom", Address: "alpha@example.test", Latitude: &lat, Longitude: &lon},
		{Name: "Fixture Room Gamma", Kind: "resource", Address: "gamma@example.test"},
		{Name: "Fixture Room Delta", Kind: "structured", LocationType: "room"},
		{Name: "Fixture Room Epsilon", Kind: "resource", Address: "epsilon@example.test"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestRoomsDedupesByName(t *testing.T) {
	e := rich(t, t2)
	e.Location = "Room One;ROOM ONE; room one"
	e.LocationsJSON = `[{"name":"Room One"},{"name":"room one"}]`
	e.AttendeesJSON = `[{"name":"ROOM one","type":"Resource","address":"one@example.test"}]`
	got := Rooms(e)
	if len(got) != 1 || got[0].Name != "Room One" || got[0].Kind != "structured" {
		t.Fatalf("%+v", got)
	}
	if got[0].Address != "one@example.test" {
		t.Fatalf("a later entry with an address fills a bare one: %+v", got)
	}
}

func TestRoomsStaleOnlyForOlderDetail(t *testing.T) {
	e := rich(t, t2)
	e.Location = "Plain Text Room"
	// Detail as of T2, row modified T2: full, nothing stale.
	e.DetailAsOf = at(t, t2)
	for _, r := range Rooms(e) {
		if r.Stale {
			t.Fatalf("full detail must not be stale: %+v", r)
		}
	}
	// The row moved on: structured and resource entries are stale, text is not.
	e.LastModified = at(t, t3)
	e.AttendeesJSON = `[{"name":"Room R","type":"Resource"}]`
	got := Rooms(e)
	stale := map[string]bool{}
	for _, r := range got {
		stale[r.Name] = r.Stale
	}
	if len(got) != 3 || stale["Plain Text Room"] || !stale["Fixture Room Alpha"] || !stale["Room R"] {
		t.Fatalf("%+v", got)
	}
}

func TestRoomsIgnoresBadJSONAndEmpty(t *testing.T) {
	e := Event{Location: "", LocationsJSON: "{not json", AttendeesJSON: "nope"}
	if got := Rooms(e); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	e = Event{LocationsJSON: `[{"name":""},{"name":"  "}]`, AttendeesJSON: `[{"name":"","type":"Resource"}]`}
	if got := Rooms(e); len(got) != 0 {
		t.Fatalf("nameless entries are skipped: %+v", got)
	}
}

func TestDetailLevel(t *testing.T) {
	e := thin(t, t2)
	if DetailLevel(e) != "basic" {
		t.Fatal("never captured must be basic")
	}
	e.DetailAsOf = at(t, t1)
	if DetailLevel(e) != "stale" {
		t.Fatal("captured before the last change must be stale")
	}
	e.DetailAsOf = at(t, t2)
	if DetailLevel(e) != "full" {
		t.Fatal("captured at the last change must be full")
	}
	e.LastModified = nil
	if DetailLevel(e) != "full" {
		t.Fatal("no row time means nothing newer to be stale against")
	}
}

// A newer rich copy changes the location text and carries no list: the older list is kept but
// marked stale although the event's detail level is full.
func TestRoomsMarkAListOlderThanTheTextStale(t *testing.T) {
	old := rich(t, t1)
	old.Location = "Fixture Room Alpha"
	old.LocationsJSON = `[{"name":"Fixture Room Alpha","kind":"conferenceRoom"},{"name":"Fixture Room Gamma","kind":"conferenceRoom"}]`
	stored := mustCapture(t, nil, old)
	moved := rich(t, t3)
	moved.Location, moved.LocationsJSON = "Fixture Room Beta", ""
	stored = mustCapture(t, &stored, moved)
	if DetailLevel(stored) != DetailFull || stored.LocationsJSON != old.LocationsJSON {
		t.Fatalf("setup: level %s list %q", DetailLevel(stored), stored.LocationsJSON)
	}
	for _, r := range Rooms(stored) {
		if r.Kind == RoomStructured && !r.Stale {
			t.Errorf("structured room %q is current but its list predates the text", r.Name)
		}
	}
	// A list as new as the text is not stale.
	for _, r := range Rooms(mustCapture(t, nil, old)) {
		if r.Stale {
			t.Errorf("fresh room %q marked stale", r.Name)
		}
	}
	older := stored
	older.Location = "Fixture Room Alpha; Fixture Room Gamma; https://teams.example.test/join"
	for _, r := range Rooms(older) {
		if r.Stale {
			t.Errorf("text that matches the list implies no change: %q stale", r.Name)
		}
	}
	// No clocks, or a list newer than the text, say nothing.
	if structuredOlderThanText(Event{Location: "x", LocationsJSON: old.LocationsJSON}) {
		t.Error("no clocks")
	}
}

func TestStructuredOlderThanTextComparesNames(t *testing.T) {
	clocks := `{"location":"2026-10-03T10:00:00.000Z","location_list":"2026-10-01T10:00:00.000Z"}`
	list := `[{"name":"Alpha"},{"name":""}]`
	for text, want := range map[string]bool{
		"Alpha":                   false,
		"alpha; https://x.test/j": false,
		"Beta":                    true,
		"Alpha; Beta":             true,
	} {
		e := Event{Location: text, LocationsJSON: list, FieldClocksJSON: clocks}
		if got := structuredOlderThanText(e); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

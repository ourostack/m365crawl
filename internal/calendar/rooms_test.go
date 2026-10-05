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

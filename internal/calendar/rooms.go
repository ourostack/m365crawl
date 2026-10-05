package calendar

import (
	"encoding/json"
	"strings"
)

// Detail levels. They say how current an event's detail group is, so a reader never mistakes an
// old attendee list, room list or body for the event as it stands now.
const (
	DetailBasic = "basic" // detail was never captured
	DetailStale = "stale" // captured, but the event changed since
	DetailFull  = "full"  // captured at the event's last modification
)

// DetailLevel reports how current e's detail is, comparing DetailAsOf with LastModified.
func DetailLevel(e Event) string {
	switch {
	case e.DetailAsOf == nil:
		return DetailBasic
	case e.LastModified != nil && e.DetailAsOf.Before(*e.LastModified):
		return DetailStale
	}
	return DetailFull
}

// Room kinds: where an entry in Rooms came from.
const (
	RoomText       = "text"       // a piece of the location text
	RoomStructured = "structured" // an entry of locations_json
	RoomResource   = "resource"   // an attendee of type Resource
)

// Room is one place an event is held, from any of the three places a source names one.
type Room struct {
	Name         string
	Kind         string
	LocationType string // the source's own type for a structured entry, for example "conferenceRoom"
	Address      string
	Latitude     *float64
	Longitude    *float64
	// Stale marks a structured or resource entry whose detail is older than the event: a room
	// change in the newer location text does not clear the older entry.
	Stale bool
}

type structuredLocation struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

type attendeeEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Type    string `json:"type"`
}

// Rooms builds one room list from the three places Teams names rooms, de-duplicated by
// case-folded name: (1) each ";"-separated piece of Location that is not a join URL, kind text;
// (2) each LocationsJSON entry, kind structured; (3) each attendee of type Resource, kind
// resource. A structured or resource entry fills a text entry of the same name, taking its kind,
// and fills an earlier entry's missing address. Structured and resource entries carry Stale when
// the detail is older than the event. Entries without a name are skipped, and JSON that does not
// parse contributes nothing.
func Rooms(e Event) []Room {
	var rooms []Room
	index := map[string]int{}
	stale := DetailLevel(e) == DetailStale
	add := func(r Room) {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			return
		}
		r.Name = name
		key := strings.ToLower(name)
		i, seen := index[key]
		if !seen {
			index[key] = len(rooms)
			rooms = append(rooms, r)
			return
		}
		cur := &rooms[i]
		if cur.Kind == RoomText {
			cur.Kind, cur.Stale = r.Kind, r.Stale
		}
		if cur.LocationType == "" {
			cur.LocationType = r.LocationType
		}
		if cur.Address == "" {
			cur.Address = r.Address
		}
		if cur.Latitude == nil {
			cur.Latitude, cur.Longitude = r.Latitude, r.Longitude
		}
	}
	for _, piece := range strings.Split(e.Location, ";") {
		piece = strings.TrimSpace(piece)
		lower := strings.ToLower(piece)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			continue
		}
		add(Room{Name: piece, Kind: RoomText})
	}
	var locations []structuredLocation
	_ = json.Unmarshal([]byte(e.LocationsJSON), &locations)
	for _, l := range locations {
		add(Room{Name: l.Name, Kind: RoomStructured, LocationType: l.Kind, Address: l.Address,
			Latitude: l.Latitude, Longitude: l.Longitude, Stale: stale})
	}
	var attendees []attendeeEntry
	_ = json.Unmarshal([]byte(e.AttendeesJSON), &attendees)
	for _, a := range attendees {
		if strings.EqualFold(a.Type, "resource") {
			add(Room{Name: a.Name, Kind: RoomResource, Address: a.Address, Stale: stale})
		}
	}
	return rooms
}

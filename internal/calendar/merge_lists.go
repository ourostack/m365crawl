package calendar

import (
	"encoding/json"
	"strings"
	"time"
)

// listAttendee is one attendee as either source stores it. Outlook has no role, so its entries
// carry an empty one once a list is rewritten by the union.
type listAttendee struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Role     string `json:"role"`
	Response string `json:"response"`
}

// listRoom is one structured location as either source stores it.
type listRoom struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// attendeeKey is the identity of an attendee across copies: the lower-case address, or the
// lower-case display name when the entry has no address.
func attendeeKey(a listAttendee) string {
	if addr := strings.ToLower(strings.TrimSpace(a.Address)); addr != "" {
		return addr
	}
	return "\x00" + strings.ToLower(strings.TrimSpace(a.Name))
}

// roomKey is the identity of a structured location across copies: the lower-case name, or the
// lower-case address when the entry has no name.
func roomKey(r listRoom) string {
	if n := strings.ToLower(strings.TrimSpace(r.Name)); n != "" {
		return n
	}
	return "\x00" + strings.ToLower(strings.TrimSpace(r.Address))
}

// unionList merges the lists of the rows that state one, newest list clock first: an entry both
// copies hold takes the newer copy's values, an entry only one holds is kept. It returns the merged
// JSON, the number of entries, and for each row index the entries it alone contributed.
func unionList[T any](rows []Event, field func(Event) string, clock func(Event) time.Time, key func(T) string) (merged string, n int, added []int) {
	type held struct {
		row  int
		list []T
	}
	var have []held
	for i, r := range rows {
		var list []T
		if json.Unmarshal([]byte(field(r)), &list) == nil && len(list) > 0 {
			have = append(have, held{i, list})
		}
	}
	if len(have) < 2 {
		return "", 0, nil
	}
	// Newest list first; a tie keeps the schedule order of the rows.
	for i := 1; i < len(have); i++ {
		for j := i; j > 0 && clock(rows[have[j].row]).After(clock(rows[have[j-1].row])); j-- {
			have[j], have[j-1] = have[j-1], have[j]
		}
	}
	var out []T
	seen := map[string]bool{}
	added = make([]int, len(rows))
	for _, h := range have {
		for _, e := range h.list {
			if k := key(e); !seen[k] {
				seen[k] = true
				out = append(out, e)
				added[h.row]++
			}
		}
	}
	data, _ := json.Marshal(out) // plain strings and numbers marshal
	return string(data), len(out), added
}

// listClock is the time a copy stated a list unit: its clock for the unit, else the copy's detail
// time, else its last modification.
func listClock(unit string) func(Event) time.Time {
	return func(e Event) time.Time {
		if t, ok := ParseFieldClocks(e.FieldClocksJSON)[unit]; ok {
			return t
		}
		if e.DetailAsOf != nil {
			return *e.DetailAsOf
		}
		if e.LastModified != nil {
			return *e.LastModified
		}
		return time.Time{}
	}
}

// mergeLists is M8: the attendee list and the structured location list are unions of the copies'
// lists by identity (see attendeeKey and roomKey), not the list of one copy. An entry both copies
// hold takes the values of the copy whose list is newer; an entry only one holds is kept. A list
// the union enlarged beyond what M6 chose is recorded as a Fill from the first copy that added
// an entry to it.
func mergeLists(out *Event, ordered []Event, fills *fillRecorder) {
	if s, n, added := unionList(ordered, func(e Event) string { return e.AttendeesJSON }, listClock("attendees"), attendeeKey); s != "" {
		if n > listLen(out.AttendeesJSON) {
			fills.add(FieldAttendees, ordered[firstAdder(added, out.AttendeesJSON, ordered, func(e Event) string { return e.AttendeesJSON })])
		}
		out.AttendeesJSON = s
	}
	if s, n, added := unionList(ordered, func(e Event) string { return e.LocationsJSON }, listClock("location_list"), roomKey); s != "" {
		if n > listLen(out.LocationsJSON) {
			fills.add(FieldRooms, ordered[firstAdder(added, out.LocationsJSON, ordered, func(e Event) string { return e.LocationsJSON })])
		}
		out.LocationsJSON = s
	}
}

// firstAdder is the first row that holds entries the chosen list lacks: a row whose list is not
// the chosen one and that contributed to the union.
func firstAdder(added []int, chosen string, rows []Event, field func(Event) string) int {
	idx := 0 // an enlarged list always has such a row; row 0 only keeps the index valid
	for i := len(rows) - 1; i >= 0; i-- {
		if added[i] > 0 && field(rows[i]) != chosen {
			idx = i
		}
	}
	return idx
}

package outlookcal

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// UnmappedError reports an event object that cannot be mapped: a field the layout says
// must decode does not. Collect counts it as the loss outlook_event_unmapped. It never
// carries a value from the store, only the reason.
type UnmappedError struct{ Reason string }

func (e *UnmappedError) Error() string { return "outlookcal: unmapped event: " + e.Reason }

// MapNotes carries facts about one mapped event that the caller counts but that are not
// errors. It never holds text from the store.
type MapNotes struct {
	// UnknownZone is the event's zone name when it resolves to no IANA id. A zone name is
	// a public Windows id, not mail or calendar content.
	UnknownZone string
	// AllDayUnaligned is set when the all-day flag was set but the instants are not
	// whole days from midnight UTC, so the event is stored as timed.
	AllDayUnaligned bool
	// EventTypeUnknown, ShowAsUnmapped and ResponseUnmapped are set when the stored code
	// is outside the values the layout lists; the field is then left unknown.
	EventTypeUnknown, ShowAsUnmapped, ResponseUnmapped bool
	// DetailMissing is set when no detail object was given; DetailUnreadable when one
	// was but a field in it did not decode.
	DetailMissing, DetailUnreadable bool
	// AttendeesUnparsed is set when the attendee list did not parse; AttendeesAtCap when
	// the list holds as many as the store's cap, so it may be truncated.
	AttendeesUnparsed, AttendeesAtCap bool
	// AttendeeResponsesUnmapped counts attendee records whose response code is not one
	// the layout lists.
	AttendeeResponsesUnmapped int
	// Redacted counts the credential-shaped values the scrubber removed.
	Redacted int
}

// MapEvent maps one class 0x6b object, with the class 0x6c detail object it links to (nil
// when none was found), to a core event. account is the calendar partition key.
//
// Mapped from the object: the identity (the id text, upper-cased, equals the Teams
// iCalUID), the event type, start, end and last modified, subject, location, organizer
// and address, response, show-as, the all-day, cancelled and online flags, the zone name
// and its IANA id, the body preview and the attendees. From the detail object: the join
// link, the toll number, the meeting thread id parsed from the link, and the body. Every
// other field is unknown (UnlocatedFields).
func MapEvent(account string, ev hxstore.Object, detail *hxstore.Object) (calendar.Event, MapNotes, error) {
	var notes MapNotes
	if ev.Class != classEvent || ev.Tag != tagEvent || ev.Len() < evFixed {
		return calendar.Event{}, notes, &UnmappedError{Reason: "not a known event layout"}
	}
	r := reader{o: ev, notes: &notes}
	area, _ := ev.U32(evAreaOne)
	base := evFixed + int(area)
	if base > ev.Len() {
		return calendar.Event{}, notes, &UnmappedError{Reason: "string area outside the object"}
	}
	raw, ok := eventID(ev)
	if !ok {
		return calendar.Event{}, notes, &UnmappedError{Reason: "id outside the object"}
	}
	uid, ok := idText(raw)
	if !ok {
		return calendar.Event{}, notes, &UnmappedError{Reason: "id is not UTF-16 hex text"}
	}
	start, okS := ev.Ticks(evStart)
	end, okE := ev.Ticks(evEnd)
	mod, okM := ev.Ticks(evLastMod)
	switch {
	case !okS || !okE:
		return calendar.Event{}, notes, &UnmappedError{Reason: "start or end is not a time"}
	case end.Before(start):
		return calendar.Event{}, notes, &UnmappedError{Reason: "end is before start"}
	case !okM:
		return calendar.Event{}, notes, &UnmappedError{Reason: "last modified is not a time"}
	}
	series := uid
	if s, _, split := calendar.SplitOccurrenceID(uid); split {
		series = s
	}
	e := calendar.Event{
		Source: calendar.SourceOutlook, AccountID: account,
		SourceID: uid, GlobalID: uid, ICalUID: uid, SeriesKey: series,
		Start: start, End: end, LastModified: &mod,
		AllDay:          flagBit(ev, evFlagsA, bitAllDay),
		Cancelled:       flagBit(ev, evFlagsA, bitCancelled),
		IsOnlineMeeting: flagBit(ev, evFlagsB, bitOnline),
	}
	unknown := append([]calendar.Field(nil), UnlocatedFields...)

	var okAll = true
	text := func(wordOff int) string {
		s, ok := ev.StringUnaligned(wordOff, base)
		okAll = okAll && ok
		return r.scrub(s)
	}
	e.Subject, e.Location = text(evSubject), text(evLocation)
	e.Organizer, e.OrganizerAddress = text(evOrgName), text(evOrgAddr)
	e.BodyPreview = text(evPreview)
	if !okAll {
		return calendar.Event{}, notes, &UnmappedError{Reason: "a string is out of range"}
	}

	e.EventType, notes.EventTypeUnknown = eventType(ev)
	e.ShowAs, notes.ShowAsUnmapped = showAs(ev)
	e.Response, notes.ResponseUnmapped = response(ev)
	if notes.ShowAsUnmapped {
		unknown = append(unknown, calendar.FieldShowAs)
	}
	if notes.ResponseUnmapped {
		unknown = append(unknown, calendar.FieldResponse)
	}

	// The zone name sits in area one, the other string base.
	zone, zoneOK := ev.StringUnaligned(evZoneName, evFixed)
	if !zoneOK || zone == "" {
		unknown = append(unknown, calendar.FieldTimeZone, calendar.FieldTimeZoneIANA)
	} else {
		e.TimeZone = zone
		if iana, _, ok := ZoneIANA(zone); ok {
			e.TimeZoneIANA = iana
		} else {
			notes.UnknownZone = zone
			unknown = append(unknown, calendar.FieldTimeZoneIANA)
		}
	}

	if e.AllDay.Is(true) {
		if s, en, ok := allDayDates(start, end); ok {
			e.StartDate, e.EndDate = s, en
		} else {
			e.AllDay = calendar.TriFalse // stored as timed, as the Teams mapper does
			notes.AllDayUnaligned = true
		}
	}

	if list, count, ok := attendees(ev, base, &r); ok && count > 0 {
		e.AttendeesJSON = list
		notes.AttendeesAtCap = count >= AttendeeCap
		e.DetailRawJSON = attendeeNote(count)
	} else {
		unknown = append(unknown, calendar.FieldAttendees)
		notes.AttendeesUnparsed = !ok
	}

	unknown = r.detail(&e, detail, unknown)
	e.Unknown = calendar.NormalizeUnknown(unknown)
	e.UnknownDeclared = true // every vocabulary field is stated or listed above
	if err := validate(e); err != nil {
		return calendar.Event{}, notes, &UnmappedError{Reason: err.Error()}
	}
	return e, notes, nil
}

// validate is the core's check, a seam so a test can reach the refusal: by construction a
// mapped event passes it.
var validate = calendar.ValidateEvent

// reader carries the object and the notes through the field readers.
type reader struct {
	o     hxstore.Object
	notes *MapNotes
}

// scrub passes s through the same scrubber the Teams derivation uses, on its JSON string
// form, and counts what it removed.
func (r reader) scrub(s string) string {
	if s == "" {
		return s
	}
	in, _ := json.Marshal(s) // a string always marshals
	out, n := teamsdesktop.Scrub(in)
	r.notes.Redacted += n
	var back string
	_ = json.Unmarshal(out, &back) // the scrubber returns a JSON string for a JSON string
	return back
}

// idText decodes the stored id: the upper-case hex text of the iCal UID bytes in UTF-16LE
// with a two-byte terminator (the length word counts them). The result is the form the
// Teams iCalUID has. ok is false for anything else: an odd length, no terminator, a
// character that is not a hex digit, or an odd number of digits.
func idText(raw []byte) (string, bool) {
	n := len(raw)
	if n < 4 || n%2 != 0 || raw[n-1] != 0 || raw[n-2] != 0 {
		return "", false
	}
	text := strings.ToUpper(utf16Text(raw[:n-2]))
	if len(text)%2 != 0 {
		return "", false
	}
	if _, err := hex.DecodeString(text); err != nil {
		return "", false
	}
	return text, true
}

// eventID returns the stored id bytes: the word at +820 is relative to +1109 and the length
// word at +824 counts bytes, terminator included. ok is false when the id is empty or outside the object; an
// id-less stub has length zero and is reported by IDLength instead.
func eventID(o hxstore.Object) ([]byte, bool) {
	off, _ := o.U32(evID)
	n, _ := o.U32(evID + 4)
	if n == 0 {
		return nil, false
	}
	return o.Bytes(evFixed+int(off), int(n))
}

// HasID reports whether an event object carries an id (id-less stubs have none).
func HasID(o hxstore.Object) bool {
	n, ok := o.U32(evID + 4)
	return ok && n != 0
}

func flagBit(o hxstore.Object, off int, mask byte) calendar.Tri {
	b, ok := o.U8(off)
	if !ok {
		return calendar.TriUnknown
	}
	return calendar.TriOf(b&mask != 0)
}

func eventType(o hxstore.Object) (string, bool) {
	v, _ := o.U32(evType)
	switch v {
	case 0:
		return calendar.EventSingle, false
	case 1:
		return calendar.EventOccurrence, false
	case 2:
		return calendar.EventException, false
	case 3:
		return calendar.EventMaster, false
	}
	return "", true
}

func showAs(o hxstore.Object) (string, bool) {
	v, _ := o.U32(evShowAs)
	switch v {
	case 0:
		return "free", false
	case 1:
		return "tentative", false
	case 2:
		return "busy", false
	}
	return "", true
}

func response(o hxstore.Object) (string, bool) {
	v, _ := o.U32(evResponse)
	switch v {
	case 0:
		return "accepted", false
	case 1:
		return "tentative", false
	case 4:
		return "none", false
	}
	return "", true
}

// allDayDates gives the date span of an all-day event: the store puts all-day events at
// midnight UTC. ok is false when the start is not midnight UTC. The end date is
// exclusive, the start plus the span rounded to whole days, at least one.
func allDayDates(start, end time.Time) (startDate, endDate string, ok bool) {
	s := start.UTC()
	if s.Hour() != 0 || s.Minute() != 0 || s.Second() != 0 || s.Nanosecond() != 0 {
		return "", "", false
	}
	days := max(1, int(math.Round(end.Sub(start).Hours()/24)))
	return s.Format(time.DateOnly), s.AddDate(0, 0, days).Format(time.DateOnly), true
}

type attendee struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Response string `json:"response"`
}

// attendees reads the list that follows the string at +876: a u32 count, then records of
// a one-byte name length, the name, a one-byte address length, the address and three u32
// words A, B and C, up to the end of the object. ok is false when the list does not parse
// exactly to the object's end. B is the response; A is 1 where Teams says optional
// (likely).
func attendees(o hxstore.Object, base int, r *reader) (list string, count int, ok bool) {
	w, _ := o.U32(evSubjectBare)
	pos, ok := stringEnd(o, base+int(w))
	if !ok {
		return "", 0, false
	}
	n, ok := o.U32(pos)
	// Each record is at least 14 bytes; a count the object cannot hold is damage.
	if !ok || int64(n)*14 > int64(o.Len()-pos-4) {
		return "", 0, false
	}
	pos += 4
	var out []attendee
	for i := 0; i < int(n); i++ {
		var a attendee
		for k := 0; k < 2; k++ {
			l, ok := o.U8(pos)
			if !ok || l%2 != 0 {
				return "", 0, false
			}
			b, ok := o.Bytes(pos+1, int(l))
			if !ok {
				return "", 0, false
			}
			pos += 1 + int(l)
			text := r.scrub(utf16Text(b))
			if k == 0 {
				a.Name = text
			} else {
				a.Address = text
			}
		}
		av, _ := o.U32(pos)
		bv, ok1 := o.U32(pos + 4)
		_, ok2 := o.U32(pos + 8)
		if !ok1 || !ok2 {
			return "", 0, false
		}
		pos += 12
		if av == 1 {
			a.Type = "optional"
		}
		switch bv {
		case 0:
			a.Response = "accepted"
		case 1:
			a.Response = "tentative"
		case 2:
			a.Response = "declined"
		case 4:
			a.Response = "none"
		default:
			r.notes.AttendeeResponsesUnmapped++
		}
		out = append(out, a)
	}
	if pos != o.Len() {
		return "", 0, false
	}
	if len(out) == 0 {
		return "", 0, true
	}
	data, _ := json.Marshal(out) // plain strings marshal
	return string(data), len(out), true
}

// stringEnd returns the offset just past the NUL terminator of the UTF-16 string at off,
// scanning in two-byte steps from off whatever its parity.
func stringEnd(o hxstore.Object, off int) (int, bool) {
	for i := off; i+1 < o.Len(); i += 2 {
		if o.Raw[i] == 0 && o.Raw[i+1] == 0 {
			return i + 2, true
		}
	}
	return 0, false
}

func utf16Text(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}

// attendeeNote is the statement that the attendee list is what the store holds, which may
// be capped: the stored count, and whether the list is as long as the cap. It invents no
// total.
func attendeeNote(stored int) string {
	data, _ := json.Marshal(struct {
		Source              string `json:"source"`
		AttendeesStored     int    `json:"attendees_stored"`
		AttendeeCap         int    `json:"capped_from"`
		AttendeesMaybeShort bool   `json:"attendees_maybe_truncated"`
	}{"outlook", stored, AttendeeCap, stored >= AttendeeCap})
	return string(data)
}

// detail fills the join link, toll number, thread id and body from the linked detail
// object, and adds to unknown the fields it cannot state. With no detail object, or one
// that does not decode, those fields are unknown.
func (r reader) detail(e *calendar.Event, d *hxstore.Object, unknown []calendar.Field) []calendar.Field {
	detailFields := []calendar.Field{calendar.FieldJoinURL, calendar.FieldDialIn, calendar.FieldMeetingChatID, calendar.FieldBody}
	if d == nil {
		r.notes.DetailMissing = true
		return append(unknown, detailFields...)
	}
	link, dial, body, ok := readDetail(*d)
	if !ok {
		r.notes.DetailUnreadable = true
		return append(unknown, detailFields...)
	}
	e.OnlineMeetingURL = r.scrub(link)
	e.TeamsThreadID = calendar.ParseTeamsThreadID(e.OnlineMeetingURL)
	e.DialInTollNumber = r.scrub(dial)
	if dial == "" {
		// The conference id is not located, so an empty toll number does not say there is no dial-in.
		unknown = append(unknown, calendar.FieldDialIn)
	}
	e.BodyHTML, e.BodyType = r.scrub(body), "html"
	e.BodyText = teamsdesktop.HTMLToText(e.BodyHTML)
	return unknown
}

// readDetail reads the join link, the toll number and the HTML body of a class 0x6c
// object. A length word of zero is an empty field. ok is false when a field is outside the
// object or the body is not UTF-8.
func readDetail(d hxstore.Object) (link, dial, body string, ok bool) {
	area, _ := d.U32(dtAreaOne)
	base := tagDetail + int(area)
	if d.Class != classDetail || d.Tag != tagDetail || d.Len() < tagDetail || base > d.Len() {
		return "", "", "", false
	}
	str := func(wordOff int) (string, bool) {
		n, _ := d.U32(wordOff + 4) // inside the fixed region, checked above
		if n&^lengthFlag == 0 {
			return "", true
		}
		return d.StringUnaligned(wordOff, base)
	}
	var okL, okD bool
	link, okL = str(dtJoinLink)
	dial, okD = str(dtDialIn)
	off, _ := d.U32(dtBody)
	n, _ := d.U32(dtBody + 4)
	n &^= lengthFlag
	b, okB := d.Bytes(base+int(off), int(n))
	if !okL || !okD || !okB || !utf8.Valid(b) {
		return "", "", "", false
	}
	return link, dial, string(b), true
}

// DetailKey is the word at +20 of a detail object, which an event's +180 word equals.
func DetailKey(d hxstore.Object) (uint32, bool) { return d.U32(dtKey) }

// detailLink is the word at +180 of an event.
func detailLink(o hxstore.Object) (uint32, bool) { return o.U32(evDetailLink) }

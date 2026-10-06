package calendar

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// clock is how an incoming copy's modification time compares with a stored reference time.
type clock int

const (
	// peers: either time is missing, so the copies cannot be ordered. The incoming copy was read
	// from the cache just now, so it is treated as at least as fresh (arrival order decides; a
	// copy with no time is the one input that cannot be made order independent).
	peers clock = iota
	// tied: both times set and equal. The tie rule decides, see prefers.
	tied
	// newer: both times set and the incoming copy is strictly later.
	newer
	// stale: both times set and the incoming copy is strictly earlier.
	stale
)

func compareClock(in, ref *time.Time) clock {
	switch {
	case in == nil || ref == nil:
		return peers
	case in.After(*ref):
		return newer
	case in.Before(*ref):
		return stale
	}
	return tied
}

// laterOf returns the later of two optional times.
func laterOf(a, b *time.Time) *time.Time {
	if b != nil && (a == nil || b.After(*a)) {
		return b
	}
	return a
}

func byteLen(s string) int { return len(s) }

// listLen counts the elements of a JSON array; anything else measures zero.
func listLen(s string) int {
	var list []json.RawMessage
	if json.Unmarshal([]byte(s), &list) != nil {
		return 0
	}
	return len(list)
}

// Field groups. Every field of Event belongs to exactly one group (TestEveryEventFieldHasAGroup
// fails for a field that is missing here), and each group is merged by its own rule:
//
//   - identity: a non-empty incoming value replaces.
//   - schedule: one unit, clocked by LastModified.
//   - detail: attendees, body (HTML, text and body type together) and the raw record; clocked
//     per unit, see units. A copy is rich only if it states attendees or a body.
//   - small: every other field a thin copy can carry on its own; clocked per unit.
//   - sticky: HasAttachments, true once any copy says so.
//   - core: maintained by Capture and the store, never taken from a copy.
const (
	groupIdentity = "identity"
	groupSchedule = "schedule"
	groupDetail   = "detail"
	groupSmall    = "small"
	groupSticky   = "sticky"
	groupCore     = "core"
)

var fieldGroups = map[string]string{
	"Source": groupIdentity, "AccountID": groupIdentity, "SourceID": groupIdentity, "GlobalID": groupIdentity,
	"ICalUID": groupIdentity, "SeriesKey": groupIdentity, "EventType": groupIdentity, "OriginalStart": groupIdentity,

	"Start": groupSchedule, "End": groupSchedule, "AllDay": groupSchedule, "StartDate": groupSchedule,
	"EndDate": groupSchedule, "TimeZone": groupSchedule, "TimeZoneIANA": groupSchedule, "UTCOffset": groupSchedule,
	"Subject": groupSchedule, "Organizer": groupSchedule, "OrganizerAddress": groupSchedule,
	"IsOrganizer": groupSchedule, "IsPrivate": groupSchedule, "Cancelled": groupSchedule, "Response": groupSchedule,
	"ShowAs": groupSchedule, "IsOnlineMeeting": groupSchedule, "LastModified": groupSchedule,

	"AttendeesJSON": groupDetail, "BodyHTML": groupDetail, "BodyText": groupDetail, "BodyType": groupDetail,
	"DetailRawJSON": groupDetail,

	"OnlineMeetingURL": groupSmall, "ShortJoinURL": groupSmall, "DialInConferenceID": groupSmall,
	"DialInTollNumber": groupSmall, "TeamsThreadID": groupSmall, "Location": groupSmall, "LocationsJSON": groupSmall,
	"BodyPreview": groupSmall, "AttachmentsJSON": groupSmall, "CategoriesJSON": groupSmall,
	"RecurrenceJSON": groupSmall, "ReminderMinutes": groupSmall,

	"HasAttachments": groupSticky,

	"ReminderStated": groupCore, "OnlineStated": groupCore, "FieldClocksJSON": groupCore, "DetailAsOf": groupCore, "DetailSeenAt": groupCore,
	"FirstSeenAt": groupCore, "SeenAt": groupCore, "RemovedAt": groupCore,
}

// scheduleFields lists the schedule group except LastModified, which is its clock.
func scheduleFields() []string {
	var out []string
	for name, g := range fieldGroups {
		if g == groupSchedule && name != "LastModified" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// unit is a set of fields that travel together and have one statement clock, stored in
// Event.FieldClocks under name.
type unit struct {
	name  string
	group string
	// family names the things that describe one subject and are listed together; it is the unit
	// name unless a unit needs two clocks (location).
	family string
	// states reports whether a copy states this unit (a reminder of zero minutes is stated; an
	// empty text is not).
	states func(Event) bool
	// measure sizes the stated value for the tie rule; larger wins.
	measure func(Event) int
	// key is the value's canonical bytes, the last tie-break; it is "" when not stated.
	key func(Event) string
	// assign copies the unit's fields from src to dst.
	assign func(dst *Event, src Event)
	// links marks the meeting links the organizer removes when the event stops being online.
	links bool
}

// fieldsUnit builds a unit over string fields of Event, named by field.
func fieldsUnit(name, group string, measure func(Event) int, fields ...string) unit {
	get := func(e *Event, f string) reflect.Value { return reflect.ValueOf(e).Elem().FieldByName(f) }
	return unit{
		name: name, family: name, group: group, measure: measure,
		states: func(e Event) bool {
			for _, f := range fields {
				if get(&e, f).String() != "" {
					return true
				}
			}
			return false
		},
		key: func(e Event) string {
			parts := make([]string, len(fields))
			for i, f := range fields {
				parts[i] = get(&e, f).String()
			}
			return strings.Join(parts, "\x00")
		},
		assign: func(dst *Event, src Event) {
			for _, f := range fields {
				get(dst, f).SetString(get(&src, f).String())
			}
		},
	}
}

func fieldMeasure(f func(string) int, field string) func(Event) int {
	return func(e Event) int { return f(reflect.ValueOf(e).FieldByName(field).String()) }
}

// units lists every clocked unit. The unit names are the keys of Event.FieldClocks; a name must
// never change once archives hold it.
//
// Reminder rule: ReminderMinutes is a nullable number and zero minutes is a real value, so "not
// stated" cannot be nil. A copy states the reminder when ReminderMinutes is non-nil or when
// ReminderStated is set, which is how a mapper says the source reported no reminder (Teams
// isReminderSet false, reminderMinutesBeforeStart null). A statement of "none" sets the reminder to
// nil and moves its clock like any other value.
//
// Fields that describe one thing share one unit, so a stored row never mixes two copies' versions
// of it: the five meeting links (a new join URL replaces the old conference id, toll number and
// thread id with it) and the location text with its structured form. Body preview stays apart
// from the body, and recurrence apart from the schedule, because thin and rich copies
// legitimately state them at different times (the Teams list view carries a preview and a
// recurrence without attendees or body).
var units = []unit{
	fieldsUnit("attendees", groupDetail, fieldMeasure(listLen, "AttendeesJSON"), "AttendeesJSON"),
	bodyUnit(),
	linksUnit(),
	locationUnit(),
	locationListUnit(),
	fieldsUnit("body_preview", groupSmall, fieldMeasure(byteLen, "BodyPreview"), "BodyPreview"),
	fieldsUnit("attachments", groupSmall, fieldMeasure(listLen, "AttachmentsJSON"), "AttachmentsJSON"),
	fieldsUnit("categories", groupSmall, nil, "CategoriesJSON"),
	fieldsUnit("recurrence", groupSmall, nil, "RecurrenceJSON"),
	reminderUnit(),
}

// linksUnit is the five meeting link fields, replaced together by the newest copy that states any
// of them. The organizer removes all of them when the meeting stops being online (captureLinks).
func linksUnit() unit {
	u := fieldsUnit("meeting_links", groupSmall, nil,
		"OnlineMeetingURL", "ShortJoinURL", "DialInConferenceID", "DialInTollNumber", "TeamsThreadID")
	u.links = true
	return u
}

// locationUnit and locationListUnit are the location text and its structured form: one family
// (they describe one place) with two clocks. Every copy states the text (an empty one means no
// location, as for the rest of the schedule). Only a copy that has one states the structured
// list. They cannot share a clock: a newer thin copy must replace the text yet keep the older
// structured rooms (Rooms marks them stale), and an older rich copy arriving later must still be
// able to supply them. Fusing the two would make the thin copy erase the rooms.
func locationUnit() unit {
	return unit{
		name: "location", family: "location", group: groupSmall,
		states: func(Event) bool { return true },
		key:    func(e Event) string { return e.Location },
		assign: func(dst *Event, src Event) { dst.Location = src.Location },
	}
}

func locationListUnit() unit {
	u := fieldsUnit("location_list", groupSmall, fieldMeasure(listLen, "LocationsJSON"), "LocationsJSON")
	u.family = "location"
	return u
}

// bodyUnit is the body: HTML, text and body type travel together, because a type has no meaning
// without the body it describes. It is stated by HTML or text; a copy carrying only a body type
// states nothing.
func bodyUnit() unit {
	u := fieldsUnit("body", groupDetail, func(e Event) int { return len(e.BodyHTML) + len(e.BodyText) },
		"BodyHTML", "BodyText", "BodyType")
	u.states = func(e Event) bool { return e.BodyHTML != "" || e.BodyText != "" }
	return u
}

func reminderUnit() unit {
	return unit{
		name: "reminder", family: "reminder", group: groupSmall,
		states: func(e Event) bool { return e.ReminderMinutes != nil || e.ReminderStated },
		key: func(e Event) string {
			switch {
			case e.ReminderMinutes != nil:
				return fmt.Sprintf("1%012d", *e.ReminderMinutes)
			case e.ReminderStated:
				return "0"
			}
			return ""
		},
		assign: func(dst *Event, src Event) {
			dst.ReminderMinutes = nil
			if src.ReminderMinutes != nil {
				n := *src.ReminderMinutes
				dst.ReminderMinutes = &n
			}
		},
	}
}

// statesDetail reports whether a copy states the substantive detail: attendees or a body.
func statesDetail(e Event) bool { return e.AttendeesJSON != "" || e.BodyHTML != "" || e.BodyText != "" }

// prefers decides between the incoming and the stored statement of a unit when the clocks cannot
// order them. The statement that says more wins (larger measure: more list elements, more text);
// when both are tied in time and still equal, the greater canonical bytes win, so the outcome
// does not depend on arrival order. When a time is missing the copies are peers and the incoming
// one wins an otherwise equal contest, as it always has.
func (u unit) prefers(base, in Event, c clock) bool {
	if u.measure != nil {
		if mb, mi := u.measure(base), u.measure(in); mb != mi {
			return mi > mb
		}
	}
	if c == tied {
		return u.key(in) >= u.key(base)
	}
	return true
}

// Capture is the append-only capture rule: it returns the row to store when in is seen and old is
// what the archive holds (nil for a first sighting). The archive keeps the richest version it has
// seen while a real change still wins.
//
//   - Identity fields take the incoming value when it is non-empty.
//   - The schedule group is compared by LastModified: a stale copy changes nothing, any other
//     copy replaces every schedule value, including an empty location and a cancelled flag.
//   - Every other field belongs to a unit (see units) with its own statement clock in
//     FieldClocks. A copy that states a unit competes for it: a newer copy replaces, an older
//     copy only fills a unit nothing has stated, and an empty incoming value never erases. So
//     each unit is decided by the newest copy that states it, whatever the arrival order.
//   - Ties are deterministic. At equal times the statement that says more wins (more list
//     elements, more text), then the greater canonical bytes of the stated value, so the same
//     copies in any order store one row. For the schedule group the greater canonical bytes of
//     its values decide. A copy with no time cannot be ordered: it competes as a peer, the
//     incoming copy wins an otherwise equal contest, and it cannot move a clock.
//   - "Not an online meeting" is a statement, not a default: a copy says it only by setting
//     OnlineStated with IsOnlineMeeting false, at its own time. That outdates every older meeting
//     link (clock no_links) and loses to a newer link, whether or not its schedule won. A copy
//     that does not set OnlineStated leaves the links alone.
//   - DetailAsOf is the newest time of a rich copy (one that states attendees or a body); a
//     thin copy, or one carrying only small fields or only a body type, never moves it.
//     DetailRawJSON follows the rich copies by its own clock and an empty one never replaces a
//     stored one.
//   - HasAttachments is true once any copy says so.
//   - An event that cannot be stored is refused (ValidateEvent) and nothing is captured.
//   - Seeing an event clears RemovedAt. FirstSeenAt, SeenAt and DetailSeenAt are the store's.
//
// Known limits. At an equal timestamp the fuller statement wins even when the shorter one is the
// truth, because equal times cannot say which was written last. A copy with no time cannot be
// ordered, so the result then depends on arrival order; mappers must always supply a time.
func Capture(old *Event, in Event) (Event, error) {
	if err := ValidateEvent(in); err != nil {
		return Event{}, err
	}
	var base Event
	if old != nil {
		base = *old
	}
	out := base
	out.ReminderStated, out.OnlineStated = false, false
	clocks := ParseFieldClocks(base.FieldClocksJSON)

	// Identity.
	for _, f := range []struct{ dst, src *string }{
		{&out.AccountID, &in.AccountID}, {&out.SourceID, &in.SourceID}, {&out.GlobalID, &in.GlobalID},
		{&out.ICalUID, &in.ICalUID}, {&out.SeriesKey, &in.SeriesKey}, {&out.EventType, &in.EventType},
	} {
		if *f.src != "" {
			*f.dst = *f.src
		}
	}
	if in.Source != "" {
		out.Source = in.Source
	}
	if in.OriginalStart != nil {
		out.OriginalStart = in.OriginalStart
	}

	// Schedule.
	sched := compareClock(in.LastModified, base.LastModified)
	applied := sched != stale && (sched != tied || scheduleKey(in) >= scheduleKey(base))
	if applied {
		src, dst := reflect.ValueOf(in), reflect.ValueOf(&out).Elem()
		for _, f := range scheduleFields() {
			dst.FieldByName(f).Set(src.FieldByName(f))
		}
		out.LastModified = laterOf(base.LastModified, in.LastModified)
	}

	// Units.
	for _, u := range units {
		if !u.states(in) {
			continue
		}
		ref, has := clocks[u.name]
		var refp *time.Time
		if has {
			refp = &ref
		}
		var take bool
		switch c := compareClock(in.LastModified, refp); {
		case !has && !u.states(base):
			take = true
		case c == newer:
			take = true
		case c == stale:
			take = false
		default:
			take = u.prefers(base, in, c)
		}
		if take {
			u.assign(&out, in)
			if t := laterOf(refp, in.LastModified); t != nil {
				clocks[u.name] = *t
			}
		}
	}
	captureRaw(&out, base, in, clocks)
	captureLinks(&out, in, clocks)
	out.HasAttachments = base.HasAttachments || in.HasAttachments

	if statesDetail(in) && in.LastModified != nil {
		out.DetailAsOf = laterOf(base.DetailAsOf, in.LastModified)
	}
	out.FieldClocksJSON = formatClocks(clocks)
	out.RemovedAt = nil
	return out, nil
}

// noLinks is the clock key of the newest copy that said the event is not an online meeting.
const noLinks = "no_links"

// captureLinks applies "the event is not an online meeting". The core cannot tell an absent flag
// from false, so only a copy that sets OnlineStated and has IsOnlineMeeting false says it; any
// other copy leaves the links alone. The organizer removes the meeting links when they turn the
// meeting off, so a copy that says so, at its time,
// outdates every link stated before that time: a link whose clock is older than the newest such
// copy reads as none, in any arrival order, and a link stated later comes back. A copy with no time
// cannot be ordered, so its "not online" clears the stored links at once, as it always has.
func captureLinks(out *Event, in Event, clocks map[string]time.Time) {
	var cleared *time.Time
	if t, ok := clocks[noLinks]; ok {
		cleared = &t
	}
	says := in.OnlineStated && !in.IsOnlineMeeting
	if says && in.LastModified != nil {
		cleared = laterOf(cleared, in.LastModified)
		clocks[noLinks] = *cleared
	}
	for _, u := range units {
		if !u.links {
			continue
		}
		stated, has := clocks[u.name]
		if (says && in.LastModified == nil) || (cleared != nil && (!has || stated.Before(*cleared))) {
			u.assign(out, Event{})
		}
	}
}

// captureRaw merges the raw detail record. A rich copy that carries one states it; the record has
// its own clock ("raw") and the same tie rule, with the larger record preferred at equal times.
func captureRaw(out *Event, base, in Event, clocks map[string]time.Time) {
	if !statesDetail(in) || in.DetailRawJSON == "" {
		return
	}
	ref, has := clocks["raw"]
	var refp *time.Time
	if has {
		refp = &ref
	}
	take := false
	switch c := compareClock(in.LastModified, refp); {
	case base.DetailRawJSON == "", c == newer, c == peers:
		take = true
	case c == stale:
	case len(in.DetailRawJSON) != len(base.DetailRawJSON):
		take = len(in.DetailRawJSON) > len(base.DetailRawJSON)
	default:
		take = in.DetailRawJSON >= base.DetailRawJSON
	}
	if take {
		out.DetailRawJSON = in.DetailRawJSON
		if t := laterOf(refp, in.LastModified); t != nil {
			clocks["raw"] = *t
		}
	}
}

// ParseFieldClocks reads Event.FieldClocksJSON into unit name to clock; an empty or unreadable
// value reads as no clocks.
func ParseFieldClocks(s string) map[string]time.Time {
	var c clockMap
	if c.Scan(s) != nil || c.m == nil {
		return map[string]time.Time{}
	}
	return c.m
}

// scheduleKey is the canonical bytes of the schedule group, for the tie rule.
func scheduleKey(e Event) string {
	v := reflect.ValueOf(e)
	var b strings.Builder
	for _, f := range scheduleFields() {
		fmt.Fprintf(&b, "%s=", f)
		switch x := v.FieldByName(f).Interface().(type) {
		case time.Time:
			b.WriteString(formatTime(x))
		default:
			fmt.Fprintf(&b, "%v", x)
		}
		b.WriteByte(0)
	}
	return b.String()
}

// detailEqual reports whether two events hold the same detail and small-field values. The raw
// record is excluded, and so is HasAttachments: thin copies carry it as true without a list, so a
// change in it alone must not claim that detail was captured.
func detailEqual(a, b Event) bool {
	for _, u := range units {
		if u.key(a) != u.key(b) {
			return false
		}
	}
	return true
}

// InvalidEventError is returned for an event that cannot be stored: a timed event with a zero
// start, an all-day event without a start date, or any time outside the years 0001 to 9999 (the
// stored text could not be read back in order).
type InvalidEventError struct {
	Source   Source
	SourceID string
	Reason   string
}

func (e *InvalidEventError) Error() string {
	return fmt.Sprintf("calendar: refused event %q of source %q: %s", e.SourceID, e.Source, e.Reason)
}

// ValidateEvent returns an *InvalidEventError when e cannot be stored. Zero times are allowed
// where the model allows them (an unset End, LastModified or OriginalStart).
func ValidateEvent(e Event) error {
	refuse := func(reason string) error {
		return &InvalidEventError{Source: e.Source, SourceID: e.SourceID, Reason: reason}
	}
	if e.AllDay {
		if e.StartDate == "" {
			return refuse("all-day event without a start date")
		}
		if d, err := time.Parse(dateLayout, e.StartDate); err != nil || d.Format(dateLayout) != e.StartDate {
			return refuse("all-day start date is not a calendar date in YYYY-MM-DD form")
		}
	} else if e.Start.IsZero() {
		return refuse("timed event with a zero start")
	}
	for name, t := range map[string]*time.Time{
		"start": &e.Start, "end": &e.End, "last modified": e.LastModified, "original start": e.OriginalStart,
	} {
		if t != nil && !t.IsZero() {
			if y := t.UTC().Year(); y < 1 || y > 9999 {
				return refuse(fmt.Sprintf("%s time outside the years 0001 to 9999", name))
			}
		}
	}
	return nil
}

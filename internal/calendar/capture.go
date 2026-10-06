package calendar

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
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
//   - schedule: when the event happens and what it is called. Each schedule unit (see units) is
//     clocked per unit, against the LastModified of the newest copy that stated it; because every
//     copy states every schedule field it knows, the newest copy replaces all of them, and a field
//     a newer copy does not know keeps its older known value.
//   - detail: attendees, body (HTML, text and body type together) and the raw record; clocked
//     per unit, see units. A copy is rich only if it states attendees or a body.
//   - small: every other field a thin copy can carry on its own; clocked per unit.
//   - sticky: HasAttachments, the larger of the stored and incoming value (unknown, false, true).
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

	"Unknown": groupCore, "UnknownDeclared": groupCore, "FieldClocksJSON": groupCore, "DetailAsOf": groupCore, "DetailSeenAt": groupCore,
	"FirstSeenAt": groupCore, "SeenAt": groupCore, "RemovedAt": groupCore,
}

// unit is a set of fields that travel together and have one statement clock, stored in
// Event.FieldClocksJSON under name.
type unit struct {
	name  string
	group string
	// family names the things that describe one subject and are listed together; it is the unit
	// name unless a unit needs two clocks (location).
	family string
	// names are the Unknown names of the fields the unit holds; a flag unit has none (its Tri says).
	names []Field
	// schedule marks a unit of the schedule group. Its clock is stored only when it differs from the
	// row's LastModified (see expandClocks), so a row whose copies all state everything carries none.
	schedule bool
	// states reports whether a copy states this unit (a reminder of zero minutes is stated; an
	// empty text is not; a known flag is; an unknown one is not).
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

// fieldsUnit builds a unit over string fields of Event, named by field. It states itself when any
// field is non-empty, so an empty value never erases.
func fieldsUnit(name, group string, measure func(Event) int, names []Field, fields ...string) unit {
	get := func(e *Event, f string) reflect.Value { return reflect.ValueOf(e).Elem().FieldByName(f) }
	return unit{
		name: name, family: name, group: group, measure: measure, names: names,
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

// statedTextUnit is a text field of the schedule group: a copy states it unless its source left it
// unknown, so a known empty value is a statement ("no organizer") that replaces an older one.
func statedTextUnit(name string, field string, known Field) unit {
	u := fieldsUnit(name, groupSchedule, nil, []Field{known}, field)
	u.schedule = true
	u.states = func(e Event) bool { return !e.unknown(known) }
	return u
}

// flagUnit is a flag of the schedule group, stated by a copy that knows it.
func flagUnit(name string, get func(*Event) *Tri) unit {
	return unit{
		name: name, family: name, group: groupSchedule, schedule: true,
		states: func(e Event) bool { return get(&e).Known() },
		key:    func(e Event) string { return strconv.Itoa(int(*get(&e))) },
		assign: func(dst *Event, src Event) { *get(dst) = *get(&src) },
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
// "reminder" is not in its Unknown: a mapper says the source reported no reminder (Teams
// isReminderSet false, reminderMinutesBeforeStart null) by leaving nil and not listing it. A
// statement of "none" sets the reminder to nil and moves its clock like any other value.
//
// Fields that describe one thing share one unit, so a stored row never mixes two copies' versions
// of it: the five meeting links (a new join URL replaces the old conference id, toll number and
// thread id with it), the location text with its structured form (two clocks, one family), the
// all-day flag with its dates, and the start with the end. Body preview stays apart from the body,
// and recurrence apart from the schedule, because thin and rich copies legitimately state them at
// different times (the Teams list view carries a preview and a recurrence without attendees or
// body).
var units = []unit{
	// Schedule group.
	whenUnit(),
	allDayUnit(),
	statedTextUnit("time_zone", "TimeZone", FieldTimeZone),
	statedTextUnit("time_zone_iana", "TimeZoneIANA", FieldTimeZoneIANA),
	statedTextUnit("utc_offset", "UTCOffset", FieldUTCOffset),
	statedTextUnit("subject", "Subject", FieldSubject),
	statedTextUnit("organizer", "Organizer", FieldOrganizer),
	statedTextUnit("organizer_address", "OrganizerAddress", FieldOrganizerAddress),
	statedTextUnit("response", "Response", FieldResponse),
	statedTextUnit("show_as", "ShowAs", FieldShowAs),
	flagUnit("is_organizer", func(e *Event) *Tri { return &e.IsOrganizer }),
	flagUnit("is_private", func(e *Event) *Tri { return &e.IsPrivate }),
	flagUnit("cancelled", func(e *Event) *Tri { return &e.Cancelled }),
	flagUnit("is_online_meeting", func(e *Event) *Tri { return &e.IsOnlineMeeting }),
	// Detail and small groups.
	fieldsUnit("attendees", groupDetail, fieldMeasure(listLen, "AttendeesJSON"), []Field{FieldAttendees}, "AttendeesJSON"),
	bodyUnit(),
	linksUnit(),
	locationUnit(),
	locationListUnit(),
	fieldsUnit("body_preview", groupSmall, fieldMeasure(byteLen, "BodyPreview"), []Field{FieldBodyPreview}, "BodyPreview"),
	fieldsUnit("attachments", groupSmall, fieldMeasure(listLen, "AttachmentsJSON"), []Field{FieldAttachments}, "AttachmentsJSON"),
	fieldsUnit("categories", groupSmall, nil, []Field{FieldCategories}, "CategoriesJSON"),
	fieldsUnit("recurrence", groupSmall, nil, []Field{FieldRecurrence}, "RecurrenceJSON"),
	reminderUnit(),
}

// unitOfName maps each Unknown name to the unit that holds the field.
var unitOfName = func() map[Field]unit {
	m := map[Field]unit{}
	for _, u := range units {
		for _, n := range u.names {
			m[n] = u
		}
	}
	return m
}()

// whenUnit is the start and end instants, which a copy always states.
func whenUnit() unit {
	return unit{
		name: "when", family: "when", group: groupSchedule, schedule: true,
		states: func(Event) bool { return true },
		key:    func(e Event) string { return formatTime(e.Start) + "|" + formatTime(e.End) },
		assign: func(dst *Event, src Event) { dst.Start, dst.End = src.Start, src.End },
	}
}

// allDayUnit is the all-day block: the flag and its dates move as one, stated by a copy that knows
// the flag. A copy that does not know it leaves the stored block alone, instants aside.
func allDayUnit() unit {
	return unit{
		name: "all_day", family: "all_day", group: groupSchedule, schedule: true,
		states: func(e Event) bool { return e.AllDay.Known() },
		key:    func(e Event) string { return strconv.Itoa(int(e.AllDay)) + "|" + e.StartDate + "|" + e.EndDate },
		assign: func(dst *Event, src Event) {
			dst.AllDay, dst.StartDate, dst.EndDate = src.AllDay, src.StartDate, src.EndDate
		},
	}
}

// linksUnit is the five meeting link fields, replaced together by the newest copy that states any
// of them. The organizer removes all of them when the meeting stops being online (captureLinks).
func linksUnit() unit {
	u := fieldsUnit("meeting_links", groupSmall, nil,
		[]Field{FieldJoinURL, FieldShortJoinURL, FieldDialIn, FieldMeetingChatID},
		"OnlineMeetingURL", "ShortJoinURL", "DialInConferenceID", "DialInTollNumber", "TeamsThreadID")
	u.links = true
	return u
}

// locationUnit and locationListUnit are the location text and its structured form: one family
// (they describe one place) with two clocks. Every copy states the text unless its source left it
// unknown (an empty one means no location, as for the rest of the schedule). Only a copy that has
// a list states the structured list. They cannot share a clock: a newer thin copy must replace the
// text yet keep the older structured rooms (Rooms marks them stale), and an older rich copy
// arriving later must still be able to supply them. Fusing the two would make the thin copy erase
// the rooms.
func locationUnit() unit {
	return unit{
		name: "location", family: "location", group: groupSmall, names: []Field{FieldLocation},
		states: func(e Event) bool { return !e.unknown(FieldLocation) },
		key:    func(e Event) string { return e.Location },
		assign: func(dst *Event, src Event) { dst.Location = src.Location },
	}
}

func locationListUnit() unit {
	u := fieldsUnit("location_list", groupSmall, fieldMeasure(listLen, "LocationsJSON"), []Field{FieldRooms}, "LocationsJSON")
	u.family = "location"
	return u
}

// bodyUnit is the body: HTML, text and body type travel together, because a type has no meaning
// without the body it describes. It is stated by HTML or text; a copy carrying only a body type
// states nothing.
func bodyUnit() unit {
	u := fieldsUnit("body", groupDetail, func(e Event) int { return len(e.BodyHTML) + len(e.BodyText) },
		[]Field{FieldBody}, "BodyHTML", "BodyText", "BodyType")
	u.states = func(e Event) bool { return e.BodyHTML != "" || e.BodyText != "" }
	return u
}

func reminderUnit() unit {
	return unit{
		name: "reminder", family: "reminder", group: groupSmall, names: []Field{FieldReminder},
		states: func(e Event) bool { return e.ReminderMinutes != nil || !e.unknown(FieldReminder) },
		key: func(e Event) string {
			if e.ReminderMinutes != nil {
				return fmt.Sprintf("1%012d", *e.ReminderMinutes)
			}
			return "0"
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

// expandClocks gives every schedule unit the row stores no clock for, and that the row knows, the
// row's LastModified: that is what an absent clock means (see unit.schedule).
func expandClocks(base Event, clocks map[string]time.Time) {
	if base.LastModified == nil {
		return
	}
	for _, u := range units {
		if _, has := clocks[u.name]; u.schedule && !has && u.states(base) {
			clocks[u.name] = *base.LastModified
		}
	}
}

// compactClocks drops the schedule clocks that equal the row's LastModified, the inverse of
// expandClocks.
func compactClocks(out Event, clocks map[string]time.Time) {
	if out.LastModified == nil {
		return
	}
	for _, u := range units {
		if t, has := clocks[u.name]; u.schedule && has && t.Equal(*out.LastModified) {
			delete(clocks, u.name)
		}
	}
}

// valued reports, for each Unknown name, whether e carries a value for it. A value is a
// statement: a copy that lists a field as unknown and also carries a value for it is read as
// knowing it, so a stray name never hides data.
var valued = map[Field]func(Event) bool{
	FieldSubject:          func(e Event) bool { return e.Subject != "" },
	FieldLocation:         func(e Event) bool { return e.Location != "" },
	FieldOrganizer:        func(e Event) bool { return e.Organizer != "" },
	FieldOrganizerAddress: func(e Event) bool { return e.OrganizerAddress != "" },
	FieldResponse:         func(e Event) bool { return e.Response != "" },
	FieldShowAs:           func(e Event) bool { return e.ShowAs != "" },
	FieldTimeZone:         func(e Event) bool { return e.TimeZone != "" },
	FieldTimeZoneIANA:     func(e Event) bool { return e.TimeZoneIANA != "" },
	FieldUTCOffset:        func(e Event) bool { return e.UTCOffset != "" },
	FieldJoinURL:          func(e Event) bool { return e.OnlineMeetingURL != "" },
	FieldShortJoinURL:     func(e Event) bool { return e.ShortJoinURL != "" },
	FieldDialIn:           func(e Event) bool { return e.DialInConferenceID != "" || e.DialInTollNumber != "" },
	FieldMeetingChatID:    func(e Event) bool { return e.TeamsThreadID != "" },
	FieldAttendees:        func(e Event) bool { return e.AttendeesJSON != "" },
	FieldRooms:            func(e Event) bool { return e.LocationsJSON != "" },
	FieldBody:             func(e Event) bool { return e.BodyHTML != "" || e.BodyText != "" },
	FieldBodyPreview:      func(e Event) bool { return e.BodyPreview != "" },
	FieldAttachments:      func(e Event) bool { return e.AttachmentsJSON != "" },
	FieldCategories:       func(e Event) bool { return e.CategoriesJSON != "" },
	FieldRecurrence:       func(e Event) bool { return e.RecurrenceJSON != "" },
	FieldReminder:         func(e Event) bool { return e.ReminderMinutes != nil },
}

// normalizeUnknown sorts e.Unknown and drops every name the copy carries a value for.
func normalizeUnknown(e *Event) {
	var keep []Field
	for _, f := range e.Unknown {
		if !valued[f](*e) {
			keep = append(keep, f)
		}
	}
	e.Unknown = NormalizeUnknown(keep)
}

// Capture is the append-only capture rule: it returns the row to store when in is seen and old is
// what the archive holds (nil for a first sighting). The archive keeps the richest version it has
// seen while a real change still wins.
//
//   - Identity fields take the incoming value when it is non-empty.
//   - Every other field belongs to a unit (see units) with its own statement clock in
//     FieldClocksJSON. A copy that states a unit competes for it: a newer copy replaces, an older
//     copy only fills a unit nothing has stated, and an empty incoming value never erases a detail
//     or small field. A schedule unit (start and end, the all-day block, each schedule text and
//     flag) is stated whenever the copy knows it, so a newer copy replaces it even with an empty
//     text or a false flag. So each unit is decided by the newest copy that states it, whatever
//     the arrival order.
//   - Unknown never replaces known and never clears: a copy does not state a flag it has as
//     TriUnknown or a field named in its Unknown (unless it also carries a value for it, which
//     counts as knowing it). A row's Unknown after capture is what no copy has stated.
//   - Ties are deterministic. At equal times the statement that says more wins (more list
//     elements, more text), then the greater canonical bytes of the stated value, so the same
//     copies in any order store one row. A copy with no time cannot be ordered: it competes as a
//     peer, the incoming copy wins an otherwise equal contest, and it cannot move a clock.
//   - "Not an online meeting" is a statement, not a default: a copy says it only by knowing
//     IsOnlineMeeting is false, at its own time. That outdates every older meeting link (clock
//     no_links) and loses to a newer link, whether or not its schedule won. A copy that does not
//     know the flag leaves the links alone.
//   - DetailAsOf is the newest time of a rich copy (one that states attendees or a body); a
//     thin copy, or one carrying only small fields or only a body type, never moves it.
//     DetailRawJSON follows the rich copies by its own clock and an empty one never replaces a
//     stored one.
//   - HasAttachments is the larger of the stored and the incoming value: true once any copy says
//     so, then false, then unknown.
//   - An event that cannot be stored is refused (ValidateEvent) and nothing is captured.
//   - Seeing an event clears RemovedAt. FirstSeenAt, SeenAt and DetailSeenAt are the store's.
//
// The all-day block (the flag and its dates) and the instants must agree. A copy that does not know
// the flag takes its instants and keeps the stored block only while the instants still look all-day
// and give the same days; otherwise the stored columns show the flag unknown and no dates, so the
// event sits on the days its instants give. The contradicted block is not lost: it rides in the
// clocks (blockStash) and shows again when the instants agree with it, whatever the arrival order.
//
// An event is refused unless its mapper set UnknownDeclared: it states that Unknown was computed,
// so an empty Unknown means "every field was read", never "the mapper forgot". The flag is input
// only and is cleared in the stored row. A known all-day event needs instants (at least one).
//
// Known limits. At an equal timestamp the fuller statement wins even when the shorter one is the
// truth, because equal times cannot say which was written last. A copy with no time cannot be
// ordered, so the result then depends on arrival order; mappers must always supply a time. A copy
// with no instants at all (a timeless copy) is refused only when it claims a known all-day event;
// UnknownDeclared says the mapper computed Unknown, not that it computed it correctly: a mapper test
// proves that with AssertEveryFieldClassified.
func Capture(old *Event, in Event) (Event, error) {
	if err := ValidateEvent(in); err != nil {
		return Event{}, err
	}
	normalizeUnknown(&in)
	var base Event
	if old != nil {
		base = *old
	}
	clocks := ParseFieldClocks(base.FieldClocksJSON)
	unstashBlock(&base, clocks)
	out := base
	expandClocks(base, clocks)

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
	out.LastModified = laterOf(base.LastModified, in.LastModified)

	// Units.
	taken := map[string]bool{}
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
		case !has && (old == nil || !u.states(base)):
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
			taken[u.name] = true
			if t := laterOf(refp, in.LastModified); t != nil {
				clocks[u.name] = *t
			}
		}
	}
	out.Unknown = capturedUnknown(old, base, in, taken)
	stashBlock(&out, clocks)
	captureRaw(&out, base, in, clocks)
	captureLinks(&out, in, clocks)
	if in.HasAttachments > out.HasAttachments {
		out.HasAttachments = in.HasAttachments
	}

	if statesDetail(in) && in.LastModified != nil {
		out.DetailAsOf = laterOf(base.DetailAsOf, in.LastModified)
	}
	compactClocks(out, clocks)
	out.FieldClocksJSON = formatClocks(clocks)
	out.RemovedAt = nil
	out.UnknownDeclared = false
	return out, nil
}

// capturedUnknown is the stored row's Unknown after in was captured over base. A field is unknown
// exactly when no copy whose unit won stated it: a unit the incoming copy took carries that copy's
// own statuses (its fields came from it), every other unit keeps the stored statuses, and a first
// copy that did not take a unit leaves its fields unknown. A copy that does not state a unit, such
// as a known-empty attendee list, cannot mark the unit's fields known.
func capturedUnknown(old *Event, base, in Event, taken map[string]bool) []Field {
	set := unknownSet{}
	for _, n := range unknownVocabulary {
		if taken[unitOfName[n].name] {
			set[n] = in.unknown(n)
		} else {
			set[n] = old == nil || base.unknown(n)
		}
	}
	for n, unknown := range set {
		if !unknown {
			delete(set, n)
		}
	}
	return set.list()
}

// noLinks is the clock key of the newest copy that said the event is not an online meeting.
const noLinks = "no_links"

// captureLinks applies "the event is not an online meeting". Only a copy that knows
// IsOnlineMeeting is false says it; an unknown flag leaves the links alone. The organizer removes
// the meeting links when they turn the meeting off, so a copy that says so, at its time,
// outdates every link stated before that time: a link whose clock is older than the newest such
// copy reads as none, in any arrival order, and a link stated later comes back. A copy with no time
// cannot be ordered, so its "not online" clears the stored links at once, as it always has.
func captureLinks(out *Event, in Event, clocks map[string]time.Time) {
	var cleared *time.Time
	if t, ok := clocks[noLinks]; ok {
		cleared = &t
	}
	says := in.IsOnlineMeeting.Is(false)
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
			// Cleared links are a statement, "none", not a gap: the organizer removed them.
			out.Unknown = without(out.Unknown, u.names...)
		}
	}
}

// without returns fields minus drop.
func without(fields []Field, drop ...Field) []Field {
	var out []Field
	for _, f := range fields {
		keep := true
		for _, d := range drop {
			keep = keep && f != d
		}
		if keep {
			out = append(out, f)
		}
	}
	return NormalizeUnknown(out)
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
	if !e.UnknownDeclared {
		return refuse("the mapper did not declare Unknown (UnknownDeclared is false)")
	}
	for _, f := range e.Unknown {
		if !validField(f) {
			return refuse(fmt.Sprintf("unknown field name %q is not in the vocabulary", f))
		}
	}
	if e.AllDay.Is(true) {
		if e.StartDate == "" {
			return refuse("all-day event without a start date")
		}
		if e.Start.IsZero() && e.End.IsZero() {
			return refuse("all-day event without instants")
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

// blockMatches reports whether an all-day block and the instants describe the same days: the
// instants look all-day, the start is within AllDayConsistencyWindow of midnight of the start date
// (zones differ), and the instants span as many whole days as the dates do. An empty or non-later
// end date means one day.
func blockMatches(e Event) bool {
	start, err := time.Parse(dateLayout, e.StartDate)
	if err != nil || !looksAllDay(e) || e.Start.Sub(start).Abs() > AllDayConsistencyWindow {
		return false
	}
	days := 1
	if end, err := time.Parse(dateLayout, e.EndDate); err == nil && end.After(start) {
		days = int((end.Sub(start) + 12*time.Hour) / (24 * time.Hour))
	}
	return days == int((e.End.Sub(e.Start)+12*time.Hour)/(24*time.Hour))
}

// blockStash is the prefix of the clock key that holds an all-day block the instants contradict.
// The stored columns show only a coherent block (the event must sit on the days its instants
// give), but the block a copy stated is still a statement: a later copy whose instants agree
// with it again, or arriving in another order, must see the same state. So the contradicted block
// rides in the clocks as "all_day_stash|<flag>|<start date>|<end date>", valued with the block's
// own clock, and Capture restores it before it decides anything.
const blockStash = "all_day_stash|"

// unstashBlock restores a stashed block over the coherent columns of base, and its clock.
func unstashBlock(base *Event, clocks map[string]time.Time) {
	for k, at := range clocks {
		rest, ok := strings.CutPrefix(k, blockStash)
		if !ok {
			continue
		}
		delete(clocks, k)
		parts := strings.SplitN(rest, "|", 3)
		if len(parts) == 3 {
			flag, _ := strconv.Atoi(parts[0])
			base.AllDay, base.StartDate, base.EndDate = Tri(flag), parts[1], parts[2] //nolint:gosec // a stash holds 0 to 2
			clocks["all_day"] = at
		}
	}
}

// stashBlock shows a coherent block: one the instants contradict is hidden (flag unknown, no
// dates) and stashed.
func stashBlock(out *Event, clocks map[string]time.Time) {
	if !out.AllDay.Is(true) || blockMatches(*out) {
		return
	}
	at := clocks["all_day"]
	clocks[blockStash+strconv.Itoa(int(out.AllDay))+"|"+out.StartDate+"|"+out.EndDate] = at
	out.AllDay, out.StartDate, out.EndDate = TriUnknown, "", ""
}

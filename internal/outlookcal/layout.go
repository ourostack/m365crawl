// Package outlookcal maps the calendar objects of the new Outlook for Mac store
// (HxStore.hxd, read through internal/hxstore) to the calendar core's events. It
// is pure: it reads objects handed to it and returns core values and counts, with
// no file access of its own beyond the reader it is given.
//
// The layout it reads is the one in docs/outlook-store.md. A field is mapped only
// when that document marks it established or likely; every other field is listed in
// UnlocatedFields and left unknown on the event (a flag stays calendar.TriUnknown, a
// vocabulary field is listed in Event.Unknown). Offsets count from the first byte of
// the object's envelope.
package outlookcal

import "github.com/ourostack/teamscrawl/internal/calendar"

// MapperVersion is raised whenever a mapping change alters derived calendar data, so
// the archive can re-read the store.
const MapperVersion = 5

// Layout names an object layout the reader knows: the class, the envelope tag (the
// size of the fixed region) and what the object is.
type Layout struct {
	Class uint16
	Tag   uint16
	Name  string
}

// The pinned layouts. A class 0x6b object with any other tag turns the source off
// (see Collect).
var (
	EventLayout  = Layout{Class: classEvent, Tag: tagEvent, Name: "event"}
	DetailLayout = Layout{Class: classDetail, Tag: tagDetail, Name: "detail"}
)

// KnownLayouts lists every (class, tag) the reader reads. Entries are added only with a
// row in the layout document and an acceptance pass.
var KnownLayouts = []Layout{EventLayout, DetailLayout, AccountLayout}

const (
	classEvent  = 0x6b
	tagEvent    = 0x455
	classDetail = 0x6c
	tagDetail   = 0x348

	// Event fixed region (docs/outlook-store.md).
	evFixed       = 1109 // the fixed size, equal to the tag
	evSeriesKey   = 20   // u64: the series key (established)
	evAreaOne     = 104  // u32: size of area one; the string area starts at evFixed + this
	evDetailLink  = 180  // u32: equals the word at dtKey of the event's detail object
	evLastMod     = 288  // ticks
	evStart       = 584  // ticks
	evEnd         = 592  // ticks
	evPreview     = 700  // string word, base T
	evZoneName    = 780  // string word, base area one (evFixed)
	evShowAs      = 816  // u32
	evID          = 820  // word (base evFixed), byte length at evID+4
	evLocation    = 836  // string word, base T
	evSubjectBare = 876  // string word, base T; the attendee list follows its terminator
	evExtraA      = 980  // string word, base T; unidentified, never mapped (an attendee list can follow it)
	evExtraB      = 772  // string word, base T; unidentified, never mapped
	evOrgName     = 884  // string word, base T
	evOrgAddr     = 892  // string word, base T
	evType        = 904  // u32
	evResponse    = 992  // u32
	evSubject     = 1024 // string word, base T
	evFlagsA      = 1082 // byte: bit 3 all-day, bit 4 cancelled
	evFlagsB      = 1083 // byte: bit 4 online meeting

	bitAllDay    = 1 << 3
	bitCancelled = 1 << 4
	bitOnline    = 1 << 4

	// Detail fixed region.
	dtKey      = 20  // u32: the detail key
	dtAreaOne  = 104 // u32: the string area starts at tagDetail + this
	dtBody     = 600 // word (base string area), length word at dtBody+4 with bit 31 set
	dtJoinLink = 700 // string word, length at dtJoinLink+4
	dtDialIn   = 728 // string word, length at dtDialIn+4

	lengthFlag = 1 << 31
)

// UnlocatedFields lists the event fields the layout document does not pin. The mapper
// leaves each unknown on every event; TestUnlocatedFieldsStayUnknown asserts it.
//
// reminder: the lead time at +448 is likely, but whether a reminder is set is not
// found, and the store holds a lead even with none, so the reminder is not stated.
var UnlocatedFields = []calendar.Field{
	calendar.FieldUTCOffset, calendar.FieldShortJoinURL, calendar.FieldRooms,
	calendar.FieldAttachments, calendar.FieldCategories, calendar.FieldRecurrence,
	calendar.FieldReminder,
}

// UnlocatedFlags lists the flags that stay unknown.
var UnlocatedFlags = []calendar.Field{
	calendar.FieldIsOrganizer, calendar.FieldIsPrivate, calendar.FieldHasAttachments,
}

// AttendeeCap is what the store holds at most: the attendee count is capped at 8 or 9,
// so a list of this many may be a truncated one.
const AttendeeCap = 8

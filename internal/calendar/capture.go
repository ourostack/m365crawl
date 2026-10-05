package calendar

import (
	"encoding/json"
	"time"
)

// clock is how an incoming copy's modification time compares with a stored reference time.
type clock int

const (
	// peers: equal, or either time missing. The incoming copy was read from the cache just now, so
	// it is treated as at least as fresh.
	peers clock = iota
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
	return peers
}

// laterOf returns the later of two optional times.
func laterOf(a, b *time.Time) *time.Time {
	if b != nil && (a == nil || b.After(*a)) {
		return b
	}
	return a
}

// detailString is one text field of the detail group. measure sizes a value for the peers rule;
// nil means "the incoming copy wins". online marks the fields the organizer removes when they turn
// the online meeting off.
type detailString struct {
	field   func(*Event) *string
	measure func(string) int
	online  bool
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

var detailStrings = []detailString{
	{field: func(e *Event) *string { return &e.OnlineMeetingURL }, online: true},
	{field: func(e *Event) *string { return &e.ShortJoinURL }, online: true},
	{field: func(e *Event) *string { return &e.DialInConferenceID }, online: true},
	{field: func(e *Event) *string { return &e.DialInTollNumber }, online: true},
	{field: func(e *Event) *string { return &e.TeamsThreadID }, online: true},
	{field: func(e *Event) *string { return &e.AttendeesJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.LocationsJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.BodyHTML }, measure: byteLen},
	{field: func(e *Event) *string { return &e.BodyText }, measure: byteLen},
	{field: func(e *Event) *string { return &e.BodyType }},
	{field: func(e *Event) *string { return &e.BodyPreview }, measure: byteLen},
	{field: func(e *Event) *string { return &e.AttachmentsJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.CategoriesJSON }},
	{field: func(e *Event) *string { return &e.RecurrenceJSON }},
}

// pickString merges one detail string by the detail clock.
func pickString(old, in string, c clock, measure func(string) int) string {
	switch {
	case in == "":
		return old
	case old == "":
		return in
	case c == newer:
		return in
	case c == stale:
		return old
	case measure != nil && measure(old) > measure(in):
		return old
	}
	return in
}

// Capture is the append-only capture rule: it returns the row to store when in is seen and old is
// what the archive holds (nil for a first sighting). The archive keeps the richest version it has
// seen while a real change still wins.
//
//   - Identity fields take the incoming value when it is non-empty.
//   - The schedule group is compared by LastModified: a stale copy changes nothing, any other
//     copy replaces every schedule value, including an empty location and a cancelled flag.
//   - The detail group is compared field by field against DetailAsOf, the modification time of the
//     copy that supplied the stored detail: an empty incoming field keeps the old value, an old
//     empty field takes the incoming one, a newer copy replaces, a stale copy keeps, and peers
//     keep the larger list or text (so a removal that arrives without a newer time is not seen).
//     The exception: a non-stale copy that says the event is not an online meeting clears the
//     meeting links it does not itself carry.
//   - DetailAsOf (and DetailRawJSON) move to the incoming copy's time when that copy changed a
//     detail field and is not older than the stored detail. A stale copy that only fills empty
//     fields leaves DetailAsOf alone, and a copy with no time cannot claim a clock.
//   - Seeing an event clears RemovedAt. FirstSeenAt, SeenAt and DetailSeenAt are the store's.
func Capture(old *Event, in Event) Event {
	var base Event
	if old != nil {
		base = *old
	}
	out := base

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
	if sched != stale {
		out.Start, out.End = in.Start, in.End
		out.AllDay, out.StartDate, out.EndDate = in.AllDay, in.StartDate, in.EndDate
		out.TimeZone, out.TimeZoneIANA, out.UTCOffset = in.TimeZone, in.TimeZoneIANA, in.UTCOffset
		out.Subject, out.Organizer, out.OrganizerAddress = in.Subject, in.Organizer, in.OrganizerAddress
		out.IsOrganizer, out.IsPrivate, out.Cancelled = in.IsOrganizer, in.IsPrivate, in.Cancelled
		out.Response, out.ShowAs = in.Response, in.ShowAs
		out.IsOnlineMeeting, out.Location = in.IsOnlineMeeting, in.Location
		out.LastModified = laterOf(base.LastModified, in.LastModified)
	}

	// Detail.
	dclock := compareClock(in.LastModified, base.DetailAsOf)
	online := sched != stale && !in.IsOnlineMeeting
	for _, d := range detailStrings {
		dst, src := d.field(&out), *d.field(&in)
		if d.online && online {
			*dst = src
			continue
		}
		*dst = pickString(*dst, src, dclock, d.measure)
	}
	out.HasAttachments = base.HasAttachments || in.HasAttachments
	out.ReminderMinutes = pickReminder(base.ReminderMinutes, in.ReminderMinutes, dclock)

	if !detailEqual(base, out) && in.LastModified != nil && dclock != stale {
		out.DetailAsOf = in.LastModified
		out.DetailRawJSON = in.DetailRawJSON
	}
	out.RemovedAt = nil
	return out
}

// pickReminder merges the nullable reminder by the detail clock.
func pickReminder(old, in *int, c clock) *int {
	switch {
	case in == nil:
		return old
	case old == nil, c != stale:
		return in
	}
	return old
}

// detailEqual reports whether two events hold the same detail values. The raw record is excluded,
// and so is HasAttachments: thin copies carry it as true without a list, so a change in it alone
// must not claim that detail was captured.
func detailEqual(a, b Event) bool {
	for _, d := range detailStrings {
		if *d.field(&a) != *d.field(&b) {
			return false
		}
	}
	if (a.ReminderMinutes == nil) != (b.ReminderMinutes == nil) {
		return false
	}
	return a.ReminderMinutes == nil || *a.ReminderMinutes == *b.ReminderMinutes
}
